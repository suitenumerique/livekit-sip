package sipbin

import (
	"fmt"
	"strconv"
	"strings"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
)

type BfcpTrack struct {
	initialized bool
	Idx         int
	Proto       string
	BfcpServer  *gst.Element
	BfcpVersion int
	ConfID      uint32
	UserID      uint16
	FloorID     uint16
}

func (e *SipBin) NewBfcpTrack(self *gst.Bin, idx int, proto string) (*BfcpTrack, error) {
	ip := e.bindIP
	if ip == nil {
		ip = e.ip
	}
	if ip == nil {
		return nil, fmt.Errorf("no IP address configured for BFCP media")
	}
	props := map[string]interface{}{
		"bind-ip": ip.String(),
	}
	if e.portStart != 0 {
		props["port-start"] = uint(e.portStart)
	}
	if e.portEnd != 0 {
		props["port-end"] = uint(e.portEnd)
	}
	bfcpServer, err := gst.NewElementWithProperties("bfcpserver", props)
	if err != nil {
		return nil, fmt.Errorf("failed to create BFCP server element: %w", err)
	}

	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)
	if _, err := bfcpServer.Connect("on-floor-released", func(_ *gst.Element, floorID, userID int) {
		if userID != int(1) {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()

		self := gst.ToGstBin(wself.Get())
		e := eweak.Value()
		if self == nil || self.Instance() == nil || e == nil {
			return
		}

		if err := e.trackToggleEvent(self, livekit.TrackSource_SCREEN_SHARE, false); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to toggle off screenshare track on floor release: %v", err))
			self.Error("Failed to toggle off screenshare track on floor release", err)
		}
	}); err != nil {
		return nil, fmt.Errorf("failed to connect on-floor-released signal: %w", err)
	}
	if _, err := bfcpServer.Connect("on-floor-granted", func(_ *gst.Element, floorID, userID, requestID int) {
		if userID != int(1) {
			return
		}

		e.mu.Lock()
		defer e.mu.Unlock()

		self := gst.ToGstBin(wself.Get())
		e := eweak.Value()
		if self == nil || self.Instance() == nil || e == nil || e.Bfcp == nil {
			return
		}

		if err := e.trackToggleEvent(self, livekit.TrackSource_SCREEN_SHARE, true); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to toggle on screenshare track on floor grant: %v", err))
			self.Error("Failed to toggle on screenshare track on floor grant", err)
		}
	}); err != nil {
		return nil, fmt.Errorf("failed to connect on-floor-granted signal: %w", err)
	}
	if _, err := bfcpServer.Connect("on-floor-requested", func(_ *gst.Element, floorID, userID, requestID int) bool {
		if userID != int(1) {
			return false
		}

		self := gst.ToGstBin(wself.Get())
		e := eweak.Value()
		if self == nil || self.Instance() == nil || e == nil || e.Bfcp == nil {
			return false
		}
		e.clearTrack(self, livekit.TrackSource_SCREEN_SHARE)
		return true
	}); err != nil {
		return nil, fmt.Errorf("failed to connect on-floor-requested signal: %w", err)
	}

	return &BfcpTrack{
		Idx:         idx,
		Proto:       proto,
		BfcpServer:  bfcpServer,
		BfcpVersion: 2,
		ConfID:      1,
		UserID:      1,
		FloorID:     1,
	}, nil
}

func (b *BfcpTrack) Init(e *SipBin, self *gst.Bin, media *gstsdp.Media, session *gstsdp.Message) error {
	if b.initialized {
		return nil
	}

	// if err := b.BfcpServer.SetProperty("floor-id", uint(b.FloorID)); err != nil {
	// 	return fmt.Errorf("failed to set floor-id property on BFCP server: %w", err)
	// }

	if version := media.GetAttributeVal("bfcpver"); version != "" {
		version, _, _ = strings.Cut(version, " ")
		if v, err := strconv.Atoi(version); err == nil {
			b.BfcpVersion = v
		} else {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to parse BFCP version from media attribute: %v", err))
		}
	}

	if err := self.Add(b.BfcpServer); err != nil {
		return fmt.Errorf("failed to add BFCP server element to bin: %w", err)
	}

	if !b.BfcpServer.SyncStateWithParent() {
		return fmt.Errorf("failed to sync state of BFCP server element with parent")
	}

	b.initialized = true
	return nil
}

func (e *SipBin) makeBfcpMedia(bfcp *BfcpTrack) (*gstsdp.Media, error) {
	media, err := gstsdp.NewMedia()
	if err != nil {
		return nil, fmt.Errorf("failed to create SDP media: %w", err)
	}

	if ret := media.SetMedia("application"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set media type on BFCP media: %v", ret)
	}

	portVal, err := bfcp.BfcpServer.GetProperty("port")
	if err != nil {
		return nil, fmt.Errorf("failed to get port property from BFCP server: %w", err)
	}
	port, ok := portVal.(uint)
	if !ok {
		return nil, fmt.Errorf("invalid type for port property from BFCP server")
	}

	// floorIDVal, err := bfcp.BfcpServer.GetProperty("floor-id")
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to get floor-id property from BFCP server: %w", err)
	// }
	// floorID, ok := floorIDVal.(uint)
	// if !ok {
	// 	return nil, fmt.Errorf("invalid type for floor-id property from BFCP server")
	// }

	if ret := media.SetPortInfo(port, 1); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set port info on BFCP media: %v", ret)
	}
	if ret := media.SetProto(bfcp.Proto); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set proto on BFCP media: %v", ret)
	}
	if ret := media.AddFormat("*"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add format to BFCP media: %v", ret)
	}

	if ret := media.AddAttribute("floorctrl", "s-only"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add floorctrl attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("bfcpver", strconv.Itoa(bfcp.BfcpVersion)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add bfcpver attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("confid", strconv.FormatUint(uint64(bfcp.ConfID), 10)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add confid attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("userid", strconv.FormatUint(uint64(bfcp.UserID), 10)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add userid attribute to BFCP media: %v", ret)
	}

	if ret := media.AddAttribute("setup", "actpass"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add setup attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("connection", "new"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add connection attribute to BFCP media: %v", ret)
	}

	return media, nil
}

func (e *SipBin) bfcpMediaAddStreams(medias []*gstsdp.Media) error {
	if e.Bfcp == nil || e.Bfcp.Idx >= len(medias) || medias[e.Bfcp.Idx] == nil {
		return nil
	}

	screenshare := e.Tracks[livekit.TrackSource_SCREEN_SHARE]
	if screenshare != nil && screenshare.Idx < len(medias) && medias[screenshare.Idx] != nil {
		label := strconv.Itoa(screenshare.Idx)
		if screenshare.Label != "" {
			label = screenshare.Label
		}
		if err := e.mediaAddBfcpLabel(medias[e.Bfcp.Idx], medias[screenshare.Idx], label); err != nil {
			return fmt.Errorf("failed to add BFCP label for screenshare: %w", err)
		}
	}

	return nil
}

func (e *SipBin) mediaAddBfcpLabel(bfcpMedia *gstsdp.Media, media *gstsdp.Media, label string) error {
	floorID := fmt.Sprintf("%d mstrm:%s", e.Bfcp.FloorID, label)
	if ret := bfcpMedia.AddAttribute("floorid", floorID); ret != gstsdp.SDPResultOk {
		return fmt.Errorf("failed to add floorid attribute to BFCP media: %v", ret)
	}
	if ret := media.AddAttribute("label", label); ret != gstsdp.SDPResultOk {
		return fmt.Errorf("failed to add label attribute to media: %v", ret)
	}
	return nil
}

func (e *SipBin) bfcpStartScreenshare(self *gst.Bin) {
	if e.Bfcp == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to start screenshare via BFCP but BFCP track is not initialized")
		return
	}

	if _, err := e.Bfcp.BfcpServer.Emit("start-screenshare", int(e.Bfcp.FloorID)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit start-screenshare signal: %v", err))
		self.Error("Failed to emit start-screenshare signal", err)
	} else {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Emitted start-screenshare signal for floor ID %d", e.Bfcp.FloorID))
	}
}

func (e *SipBin) bfcpStopScreenshare(self *gst.Bin) {
	if e.Bfcp == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to stop screenshare via BFCP but BFCP track is not initialized")
		return
	}

	if _, err := e.Bfcp.BfcpServer.Emit("stop-screenshare", int(e.Bfcp.FloorID)); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit stop-screenshare signal: %v", err))
		self.Error("Failed to emit stop-screenshare signal", err)
	} else {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Emitted stop-screenshare signal for floor ID %d", e.Bfcp.FloorID))
	}
}
