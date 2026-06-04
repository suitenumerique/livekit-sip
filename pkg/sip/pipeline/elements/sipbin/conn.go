package sipbin

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"golang.org/x/sys/unix"
)

/*
#cgo pkg-config: gio-2.0
#include <gio/gio.h>
// Helper to get the GType for GSocket
static GType get_socket_type() {
    return g_socket_get_type();
}

static GSocket* create_gsocket_from_fd(int fd) {
    GError *err = NULL;
    // g_socket_new_from_fd takes ownership of the fd
    GSocket *sock = g_socket_new_from_fd(fd, &err);

    if (err != NULL) {
        g_error_free(err);
        return NULL;
    }
    return sock;
}
*/
import "C"

var ErrListenFailed = errors.New("failed to listen on udp port")

func listenUDPWithReusePort(ip net.IP, port int) (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}

	addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))

	conn, err := lc.ListenPacket(context.Background(), "udp4", addr)
	if err != nil {
		return nil, err
	}

	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("failed to cast to *net.UDPConn")
	}

	return udpConn, nil
}

func NewUDPConnPair(portMin, portMax uint16, ip net.IP) (*net.UDPConn, *net.UDPConn, error) {
	if ip.To4() == nil {
		return nil, nil, fmt.Errorf("only IPv4 addresses are supported")
	}

	if portMin == 0 && portMax == 0 {
		portMin = 1024
		portMax = 0xFFFF
	}

	i := portMin
	if i == 0 {
		i = 1
	}
	if i%2 != 0 {
		i++
	}

	j := portMax
	if j == 0 {
		j = 0xFFFF
	}

	if i > j {
		return nil, nil, ErrListenFailed
	}

	portRange := (j - i) / 2
	if portRange <= 0 {
		portRange = 1
	}
	portStart := uint16((rand.Intn(int(portRange)) * 2)) + i
	if portStart%2 != 0 {
		portStart++
	}

	portCurrent := portStart

	for {
		rtpConn, err := listenUDPWithReusePort(ip, int(portCurrent))
		if err == nil {
			rtcpConn, err := listenUDPWithReusePort(ip, int(portCurrent+1))
			if err == nil {
				return rtpConn, rtcpConn, nil
			}
			rtpConn.Close()
		}

		portCurrent += 2
		if portCurrent > j {
			portCurrent = i
			if portCurrent%2 != 0 {
				portCurrent++
			}
		}
		if portCurrent == portStart {
			break
		}
	}
	return nil, nil, ErrListenFailed
}

type GSocketWrapper struct {
	*glib.Object
}

func (s *GSocketWrapper) ToGValue() (*glib.Value, error) {
	socketType := glib.Type(C.get_socket_type())
	val, err := glib.ValueInit(socketType)
	if err != nil {
		return nil, err
	}
	val.SetInstance(s.Object.Unsafe())
	return val, nil
}

func GSocketFromUDPConn(conn *net.UDPConn) (*GSocketWrapper, error) {
	file, err := conn.File()
	if err != nil {
		return nil, err
	}
	defer file.Close()

	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}

	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, errors.New("failed to set socket to non-blocking mode")
	}

	cSocket := C.create_gsocket_from_fd(C.int(fd))
	if cSocket == nil {
		syscall.Close(fd)
		return nil, errors.New("failed to create GSocket from UDPConn")
	}

	obj := glib.TransferFull(unsafe.Pointer(cSocket))
	return &GSocketWrapper{Object: obj}, nil
}
