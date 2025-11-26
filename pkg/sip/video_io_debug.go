package sip

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

func NewDebugWriteCloser(w io.WriteCloser, prefix string, tick time.Duration) *DebugWriteCloser {
	dw := &DebugWriteCloser{
		ctx:         context.Background(),
		WriteCloser: w,
		Prefix:      prefix,
	}

	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()

		fmt.Printf("%s debug writer started\n", dw.Prefix)
		defer fmt.Printf("%s debug writer stopped\n", dw.Prefix)
		for {
			select {
			case <-ticker.C:
				// Log the number of bytes written so far
				n := dw.n.Swap(0)
				c := dw.c.Swap(0)
				fmt.Printf("%s wrote %d times (%d bytes)\n", dw.Prefix, c, n)
			case <-dw.ctx.Done():
				return
			}
		}
	}()

	return dw
}

type DebugWriteCloser struct {
	io.WriteCloser
	ctx    context.Context
	n      atomic.Int64
	c      atomic.Int64
	Prefix string
}

func (d *DebugWriteCloser) Write(p []byte) (int, error) {
	d.c.Add(1)
	n, err := d.WriteCloser.Write(p)
	if err == nil {
		d.n.Add(int64(n))
	}
	return n, err
}

func (d *DebugWriteCloser) Close() error {
	d.ctx.Done()
	return d.WriteCloser.Close()
}

func NewDebugReadCloser(r io.ReadCloser, prefix string, tick time.Duration) *DebugReadCloser {
	dr := &DebugReadCloser{
		ctx:        context.Background(),
		ReadCloser: r,
		Prefix:     prefix,
	}

	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()

		fmt.Printf("%s debug reader started\n", dr.Prefix)
		defer fmt.Printf("%s debug reader stopped\n", dr.Prefix)
		for {
			select {
			case <-ticker.C:
				// Log the number of bytes read so far
				n := dr.n.Swap(0)
				c := dr.c.Swap(0)
				fmt.Printf("%s read %d times (%d bytes)\n", dr.Prefix, c, n)
			case <-dr.ctx.Done():
				return
			}
		}
	}()

	return dr
}

type DebugReadCloser struct {
	io.ReadCloser
	ctx    context.Context
	n      atomic.Int64
	c      atomic.Int64
	Prefix string
}

func (d *DebugReadCloser) Read(p []byte) (int, error) {
	d.c.Add(1)
	n, err := d.ReadCloser.Read(p)
	if err == nil {
		d.n.Add(int64(n))
	}
	return n, err
}

func (d *DebugReadCloser) Close() error {
	d.ctx.Done()
	return d.ReadCloser.Close()
}

var rtcpLog sync.Once

func rtcpLogFile() *os.File {
	var rtcpLogFile *os.File
	rtcpLog.Do(func() {
		var err error
		rtcpLogFile, err = os.OpenFile("rtcp.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			panic(fmt.Sprintf("failed to open rtcp.log: %v", err))
		}

		if err := rtcpLogFile.Truncate(0); err != nil {
			panic(fmt.Sprintf("failed to truncate rtcp.log: %v", err))
		}
	})
	return rtcpLogFile
}

type RtcpDebugWriter struct {
	pc *webrtc.PeerConnection
}

func (r *RtcpDebugWriter) Write(p []byte) (n int, err error) {
	fmt.Fprintf(rtcpLogFile(), "\n\nRTCP Writer got %d bytes\n", len(p))
	fmt.Fprintf(rtcpLogFile(), "%x\n", p)
	pkts, err := rtcp.Unmarshal(p)
	if err != nil {
		return 0, err
	}
	for _, pkt := range pkts {
		fmt.Fprintf(rtcpLogFile(), "RTCP Packet: %T %+v\n", pkt, pkt)
	}
	if err := r.pc.WriteRTCP(pkts); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *RtcpDebugWriter) Close() error {
	return nil
}
