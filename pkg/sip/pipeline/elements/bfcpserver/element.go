package bfcpserver

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/bfcp"
)

var CAT = gst.NewDebugCategory(
	"bfcpserver",
	gst.DebugColorBgGreen,
	"bfcpserver Element",
)

var (
	signalOnFloorRequested uint
	signalOnFloorGranted   uint
	signalOnFloorReleased  uint
	signalStartScreenshare uint
	signalStopScreenshare  uint
)

type BFCPServer struct {
	props
	bfcpServer       *bfcp.Server
	bfcpConfig       *bfcp.ServerConfig
	started          bool
	constructed      bool
	requestID        atomic.Int64
	lastFloorRelease time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (e *BFCPServer) New() glib.GoObjectSubclass {
	return &BFCPServer{}
}

func (e *BFCPServer) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"BFCPServer",
		"Generic",
		"BFCPServer Element",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	signalOnFloorRequested = gst.SignalNew(
		class.Type(),
		"on-floor-requested",
		gst.SignalRunLast,
		glib.TYPE_BOOLEAN,
		glib.TYPE_INT,
		glib.TYPE_INT,
		glib.TYPE_INT,
	)

	signalOnFloorGranted = gst.SignalNew(
		class.Type(),
		"on-floor-granted",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT,
		glib.TYPE_INT,
		glib.TYPE_INT,
	)

	signalOnFloorReleased = gst.SignalNew(
		class.Type(),
		"on-floor-released",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT,
		glib.TYPE_INT,
	)

	signalStartScreenshare = gst.SignalNew(
		class.Type(),
		"start-screenshare",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT, // floor ID
	)

	signalStopScreenshare = gst.SignalNew(
		class.Type(),
		"stop-screenshare",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		glib.TYPE_INT, // floor ID
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"noop",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (e *BFCPServer) InstanceInit(instance *glib.Object) {
	self := gst.ToElement(instance)

	e.props.floorID = 1
	e.ctx, e.cancel = context.WithCancel(context.Background())

	class := gst.ToElementClass(self.Class())

	self.AddPad(gst.NewGhostPadNoTargetFromTemplate("noop", class.GetPadTemplate("noop")).Pad)
}

func (e *BFCPServer) Constructed(instance *glib.Object) {
	self := gst.ToElement(instance)

	addr := ":0"
	if e.bindIP != nil {
		addr = e.bindIP.String() + addr
	}

	config := bfcp.DefaultServerConfig(addr, 1)
	if e.portStart != 0 {
		config.PortMin = int(e.portStart)
	}
	if e.portEnd != 0 {
		config.PortMax = int(e.portEnd)
	}

	config.Logger = NewGstLogger(self)

	config.Transport = bfcp.TransportUDP

	e.bfcpConfig = config
	e.bfcpServer = bfcp.NewServer(config)

	if err := e.bfcpServer.Listen(); err != nil {
		fallbackConfig := config
		fallbackConfig.Address = ":0"
		fallbackServer := bfcp.NewServer(fallbackConfig)
		if fallbackServer.Listen() == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to bind BFCP server to %s, but successfully bound to %s. Using fallback address.", addr, fallbackConfig.Address))
			e.bfcpConfig = fallbackConfig
			e.bfcpServer = fallbackServer
		} else {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to start BFCP server: %v", err))
			self.Error("Failed to start BFCP server", err)
			return
		}
	}

	host, portStr, err := net.SplitHostPort(e.bfcpServer.Addr().String())
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to split host and port: %v", err))
		self.Error("Failed to start BFCP server", err)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to parse port: %v", err))
		self.Error("Failed to start BFCP server", err)
		return
	}

	if port <= 0 || port > 0xFFFF {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid port number: %d", port))
		self.Error("Failed to start BFCP server", fmt.Errorf("invalid port number: %d", port))
		return
	}

	e.port = uint16(port)

	e.SetupSignals(self)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("BFCP server started on %s:%d", host, port))

	e.constructed = true
}

func (e *BFCPServer) broadcast() {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-t.C:
				for _, f := range e.bfcpServer.ListFloors() {
					e.bfcpServer.BroadcastFloorState(f.FloorID, f.GetOwner(), f.GetState())
				}
			}
		}
	}()
}

func (e *BFCPServer) ChangeState(self *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	if transition == gst.StateChangeReadyToPaused && !e.started {
		e.bfcpServer.Serve()
		// e.broadcast()
		e.started = true
	}

	ret := self.ParentChangeState(transition)
	if ret != gst.StateChangeSuccess {
		return ret
	}

	if transition == gst.StateChangeNullToReady {
		e.bfcpServer.CreateFloor(e.floorID)
	}

	if transition == gst.StateChangeReadyToNull {
		e.cancel()
		if err := e.bfcpServer.Close(); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to close BFCP server: %v", err))
		}
		e.wg.Wait()
	}
	return ret
}
