package benchmarks

// IPC element factories. The split-process runner uses a different
// GStreamer IPC element per memory type:
//
//   MemSystem -> unixfdsink / unixfdsrc
//     * Carries RTP, encoded bytes, system-memory raw video.
//     * Path: a unix domain socket.
//
//   MemCUDA   -> cudaipcsink / cudaipcsrc
//     * Carries video/x-raw(memory:CUDAMemory) zero-copy via the
//       CUDA IPC handle API. No host-side memcpy.
//     * Address: a unix domain socket for the rendezvous (same
//       shape as unixfd paths).
//
// Both IPC families use a unix socket path string for rendezvous,
// but they spell the property differently — unixfd* uses
// "socket-path", cudaipc* uses "address". The helpers below
// abstract that away.
//
// The locked-state trick for the return path (see buildMainPipeline)
// applies identically to cudaipcsrc — it is still a GstBaseSrc whose
// start() vfunc does a blocking connect.

import (
	"fmt"
	"sync"

	"github.com/go-gst/go-gst/gst"
)

// ipcSinkFor returns the IPC sink element matching the given memory
// type, configured with the given socket path. The caller is
// responsible for adding it to a pipeline and setting sync/async
// properties.
//
// cudaipcsink uses ipc-mode=mmap (1). Legacy mode (0) produces
// CUDA memory that nvh264enc (and likely other NVENC elements)
// cannot consume — "Internal data stream error" even in pure
// gst-launch. mmap mode works but requires runtime.LockOSThread()
// on the main goroutine before gst.Init in both processes — without
// it, Go's goroutine scheduler migrates the main goroutine across
// OS threads, breaking CUDA context thread affinity for the mmap
// shared-memory operations.
func ipcSinkFor(memType int, socketPath string) (*gst.Element, error) {
	switch memType {
	case MemCUDA:
		return gst.NewElementWithProperties("cudaipcsink", map[string]any{
			"address":  socketPath,
			"ipc-mode": 1, // mmap — see comment above
		})
	case MemSystem:
		return gst.NewElementWithProperties("unixfdsink", map[string]any{
			"socket-path": socketPath,
		})
	default:
		return nil, fmt.Errorf("ipcSinkFor: unknown memory type %d", memType)
	}
}

// ipcSrcFor returns the IPC source element matching the given memory
// type, configured with the given socket path.
func ipcSrcFor(memType int, socketPath string) (*gst.Element, error) {
	switch memType {
	case MemCUDA:
		return gst.NewElementWithProperties("cudaipcsrc", map[string]any{
			"address": socketPath,
		})
	case MemSystem:
		return gst.NewElementWithProperties("unixfdsrc", map[string]any{
			"socket-path": socketPath,
		})
	default:
		return nil, fmt.Errorf("ipcSrcFor: unknown memory type %d", memType)
	}
}

// memTypeName returns a short string for logging / dot filenames.
func memTypeName(memType int) string {
	switch memType {
	case MemCUDA:
		return "cuda"
	case MemSystem:
		return "sys"
	default:
		return fmt.Sprintf("mem%d", memType)
	}
}

// Both IPC families follow the same server/client convention:
//
//	unixfdsink / cudaipcsink  — server, binds the socket
//	unixfdsrc  / cudaipcsrc   — client, connects to the socket
//
// Sinks never need locking. Src elements (clients) must be locked
// until their server peer has bound the socket.
//
// For cudaipc there is an additional constraint: the cudaipcsrc
// client must not connect until the cudaipcsink server has received
// its first buffer (the "Have no config data yet" codepath in
// gstcudaipcserver.cpp — CONFIG is never sent if the client connects
// before the server has data). This is handled by the
// "sink-has-data" IPC handshake between main and child; see
// installSinkHasDataProbe.

// installSinkHasDataProbe installs a one-shot buffer pad probe on the
// sink pad of a cudaipcsink element. The returned channel is closed
// when the first buffer arrives at the pad — proof that the CUDA IPC
// server has config data and a client can now safely connect.
func installSinkHasDataProbe(sink *gst.Element) <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	pad := sink.GetStaticPad("sink")
	pad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList,
		func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			once.Do(func() { close(ch) })
			return gst.PadProbeRemove
		})
	return ch
}

// installEOSProbe installs a one-shot event probe on the sink pad
// that detects EOS events. The returned channel is closed when EOS
// reaches the pad. Used on the forward-path IPC sink to detect that
// the source has finished — since cudaipcsink (mmap mode) does not
// forward EOS via the IPC protocol, the runner must signal the child
// explicitly.
func installEOSProbe(sink *gst.Element) <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	pad := sink.GetStaticPad("sink")
	pad.AddProbe(gst.PadProbeTypeEventDownstream,
		func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			ev := info.GetEvent()
			if ev != nil && ev.Type() == gst.EventTypeEOS {
				once.Do(func() { close(ch) })
				return gst.PadProbeRemove
			}
			return gst.PadProbeOK
		})
	return ch
}
