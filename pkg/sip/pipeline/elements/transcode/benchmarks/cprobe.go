package benchmarks

// CGo wrapper for the pure-C latency pad probes. The C probe callbacks
// run entirely in C (no CGo crossing per buffer). Go only crosses the
// CGo boundary at setup time and when reading results after EOS.

/*
#cgo pkg-config: gstreamer-1.0
#include "cprobe.h"
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
	"unsafe"

	"github.com/go-gst/go-gst/gst"
)

type cProbe struct {
	ptr *C.CProbe
}

func newCProbe(capacity int) *cProbe {
	return &cProbe{ptr: C.cprobe_new(C.int(capacity))}
}

func (p *cProbe) installEntry(pad *gst.Pad) {
	C.cprobe_install_entry((*C.GstPad)(unsafe.Pointer(pad.Unsafe())), p.ptr)
}

func (p *cProbe) installExit(pad *gst.Pad) {
	C.cprobe_install_exit((*C.GstPad)(unsafe.Pointer(pad.Unsafe())), p.ptr)
}

func (p *cProbe) snapshot() []time.Duration {
	n := int(C.cprobe_latency_count(p.ptr))
	if n == 0 {
		return nil
	}
	raw := C.cprobe_latencies(p.ptr)
	arr := unsafe.Slice((*int64)(unsafe.Pointer(raw)), n)
	out := make([]time.Duration, n)
	for i, ns := range arr {
		out[i] = time.Duration(ns)
	}
	return out
}

func (p *cProbe) free() {
	if p.ptr != nil {
		C.cprobe_free(p.ptr)
		p.ptr = nil
	}
}

// writeTo writes the collected latencies to a binary file so the
// runner process can read them after the child exits.
// Format: 4-byte LE count + N × 8-byte LE int64 (nanoseconds).
func (p *cProbe) writeTo(path string) error {
	latencies := p.snapshot()
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cprobe write: %w", err)
	}
	defer f.Close()

	if err := binary.Write(f, binary.LittleEndian, int32(len(latencies))); err != nil {
		return fmt.Errorf("cprobe write count: %w", err)
	}
	for _, d := range latencies {
		if err := binary.Write(f, binary.LittleEndian, int64(d)); err != nil {
			return fmt.Errorf("cprobe write sample: %w", err)
		}
	}
	return nil
}

// readCProbeFile reads a binary latency file written by cProbe.writeTo.
func readCProbeFile(path string) ([]time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var count int32
	if err := binary.Read(f, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("cprobe read count: %w", err)
	}
	if count <= 0 {
		return nil, nil
	}
	raw := make([]int64, count)
	if err := binary.Read(f, binary.LittleEndian, &raw); err != nil {
		return nil, fmt.Errorf("cprobe read samples: %w", err)
	}
	out := make([]time.Duration, count)
	for i, ns := range raw {
		out[i] = time.Duration(ns)
	}
	return out, nil
}
