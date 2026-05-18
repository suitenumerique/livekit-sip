package iolivekit

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"golang.org/x/sys/unix"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/trackfallback"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiopcma"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiopcmu"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/factorybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/flacsource"
	"github.com/livekit/sip/res"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", glib.GetEnv("GST_DEBUG")+",iolivekit:5,flacsource:5", true)
	gst.Init(nil)

	if !trackfallback.Register() {
		panic("failed to register trackfallback")
	}
	if !livekitcompositor.Register() {
		panic("failed to register livekit_compositor")
	}
	if !audiopcmu.Register() {
		panic("failed to register audio-pcmu")
	}
	if !audiopcma.Register() {
		panic("failed to register audio-pcma")
	}
	if !factorybin.Register() {
		panic("failed to register factorybin")
	}
	if !flacsource.Register() {
		panic("failed to register flacsource")
	}
	Register()

	os.Exit(m.Run())
}

// openWavFd creates a memfd from RoomJoinWav and opens an independent per-call
// fd via /proc/self/fd/N. The masterFd is closed on test cleanup.
func openWavFd(t *testing.T) int {
	t.Helper()
	Data, err := res.EnterPin.ReadFile("lang/en/enter_pin.wav")
	if err != nil {
		t.Fatal("failed to read embedded WAV file:", err)
	}
	masterFd, err := res.MemfdFromBytes("test-wav", Data)
	if err != nil {
		t.Fatal("failed to create memfd:", err)
	}
	t.Cleanup(func() { unix.Close(masterFd) })

	playFd, err := unix.Open(
		fmt.Sprintf("/proc/self/fd/%d", masterFd),
		unix.O_RDONLY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatal("failed to open per-call fd:", err)
	}
	return playFd
}

func dumpDot(t *testing.T, pipeline *gst.Pipeline, label string) {
	t.Helper()
	dir := filepath.Join("testdata", t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("failed to create dot dir: %v", err)
		return
	}
	data := pipeline.Bin.DebugBinToDotData(gst.DebugGraphShowAll)
	path := filepath.Join(dir, label+".dot")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Logf("failed to write dot %s: %v", path, err)
	} else {
		t.Logf("wrote DOT graph to %s", path)
	}
}

// TestIoLivekit_TwoPlayAudio mirrors the production scenario:
// create iolivekit, set microphone=true so the fallback keeps the mixer's
// downstream chain alive between plays, set state PLAYING, briefly wait,
// emit play-wav-fd, wait, emit play-wav-fd again, then NULL.
//
// The output of send_rtp_src is depayloaded (PCMU), decoded (mulaw), and
// written to a WAV file at testdata/<test>/output.wav for offline inspection.
//
// As of the last attempt, this test triggers a segfault in the streaming
// thread shortly after the first play starts pushing buffers through the
// trackfallback chain. The pipeline DOT dumps + bus messages are intended to
// help diagnose the crash.
func TestIoLivekit_TwoPlayAudio(t *testing.T) {
	pipeline, err := gst.NewPipeline("test-iolivekit-two-plays")
	if err != nil {
		t.Fatal(err)
	}

	iol, err := gst.NewElement("iolivekit")
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Add(iol); err != nil {
		t.Fatal(err)
	}

	outputWav := filepath.Join("testdata", t.Name(), "output.wav")
	if err := os.MkdirAll(filepath.Dir(outputWav), 0o755); err != nil {
		t.Fatal(err)
	}

	// Counting RTP buffers downstream of the iolivekit so we can correlate
	// with first/second play windows.
	var rtpBufferCount atomic.Int32

	// When iolivekit publishes its send_rtp_src_X ghost pad, build a chain
	// that depayloads the RTP, decodes it, and writes a WAV file. The
	// factorybin in iolivekit picks PCMU or PCMA based on caps negotiation;
	// since we offer no preference, it picks the first registered factory
	// (audio-pcmu in our TestMain registration order).
	if _, err := iol.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		name := pad.GetName()
		if name != "send_rtp_src_2" {
			return
		}
		t.Logf("iolivekit added pad %s, attaching depay+decode+wavenc chain", name)

		pad.AddProbe(gst.PadProbeTypeBuffer, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
			rtpBufferCount.Add(1)
			return gst.PadProbeOK
		})

		depay, err := gst.NewElement("rtppcmudepay")
		if err != nil {
			t.Errorf("failed to create rtppcmudepay: %v", err)
			return
		}
		dec, err := gst.NewElement("mulawdec")
		if err != nil {
			t.Errorf("failed to create mulawdec: %v", err)
			return
		}
		conv, err := gst.NewElement("audioconvert")
		if err != nil {
			t.Errorf("failed to create audioconvert: %v", err)
			return
		}
		enc, err := gst.NewElement("wavenc")
		if err != nil {
			t.Errorf("failed to create wavenc: %v", err)
			return
		}
		sink, err := gst.NewElementWithProperties("filesink", map[string]interface{}{
			"location": outputWav,
		})
		if err != nil {
			t.Errorf("failed to create filesink: %v", err)
			return
		}
		if err := pipeline.AddMany(depay, dec, conv, enc, sink); err != nil {
			t.Errorf("failed to add chain: %v", err)
			return
		}
		if err := gst.ElementLinkMany(depay, dec, conv, enc, sink); err != nil {
			t.Errorf("failed to link chain: %v", err)
			return
		}
		for _, el := range []*gst.Element{depay, dec, conv, enc, sink} {
			if !el.SyncStateWithParent() {
				t.Logf("warning: failed to sync %s state with parent", el.GetName())
			}
		}
		if ret := pad.Link(depay.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Errorf("failed to link send_rtp_src to depay: %v", ret)
		}
	}); err != nil {
		t.Fatal(err)
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	// microphone=true keeps the fallback's microphone path alive even when
	// no real LiveKit RTP is feeding the mixer; this is what production gets
	// from the SipBin's available-media signal.
	if err := iol.SetProperty("microphone", true); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	dumpDot(t, pipeline, "01-after-playing")

	// Drain the bus in the background so error messages surface promptly.
	bus := pipeline.GetPipelineBus()
	stopBus := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopBus:
				return
			default:
			}
			msg := bus.TimedPop(gst.ClockTime(100 * time.Millisecond))
			if msg == nil {
				continue
			}
			if msg.Type() == gst.MessageError {
				t.Errorf("bus error: %v", msg.ParseError().Error())
			}
		}
	}()
	defer close(stopBus)

	t.Log("emitting first play-wav-fd")
	fd1 := openWavFd(t)
	start1 := time.Now()

	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("timeout waiting for first play to complete")
			dumpDot(t, pipeline, "99-timeout")
		}
	}()

	if _, err := iol.Emit("play-wav-fd", fd1); err != nil {
		t.Errorf("first play-wav-fd emit failed: %v", err)
	}
	close(done)
	dur1 := time.Since(start1)
	count1 := rtpBufferCount.Load()
	t.Logf("first play-wav-fd: duration=%s rtp_buffers=%d", dur1, count1)
	dumpDot(t, pipeline, "02-after-first-play")

	t.Log("waiting 2s between plays")
	time.Sleep(2 * time.Second)
	dumpDot(t, pipeline, "03-before-second-play")

	t.Log("emitting second play-wav-fd")
	fd2 := openWavFd(t)
	start2 := time.Now()
	if _, err := iol.Emit("play-wav-fd", fd2); err != nil {
		t.Errorf("second play-wav-fd emit failed: %v", err)
	}
	dur2 := time.Since(start2)
	count2 := rtpBufferCount.Load() - count1
	t.Logf("second play-wav-fd: duration=%s rtp_buffers=%d", dur2, count2)
	dumpDot(t, pipeline, "04-after-second-play")

	time.Sleep(1 * time.Second)

	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	// Useful diagnostic: huge difference between dur1 and dur2 suggests a hang.
	if dur2 > 3*dur1 && dur2 > 5*time.Second {
		t.Errorf("suspicious duration regression: first=%s second=%s", dur1, dur2)
	}
}
