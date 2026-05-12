// placeholder-test is a one-off harness that runs the exact GStreamer
// chain used by the SIP placeholder video, piped to a fakesink. It's the
// fastest way to confirm whether the chain itself is producing frames
// (vs. the InputSelector wiring or some other downstream issue).
package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
)

func main() {
	gst.Init(nil)

	pipeline, err := gst.NewPipeline("placeholder-test")
	if err != nil {
		fmt.Println("pipeline err:", err)
		os.Exit(1)
	}

	src, err := gst.NewElementWithProperties("filesrc", map[string]interface{}{
		"location": "/tmp/encryption_blocked.png",
	})
	if err != nil {
		fmt.Println("filesrc:", err)
		os.Exit(1)
	}
	tf, err := gst.NewElementWithProperties("typefind", map[string]interface{}{
		"force-caps": gst.NewCapsFromString("image/png"),
	})
	if err != nil {
		fmt.Println("typefind:", err)
		os.Exit(1)
	}
	dec, err := gst.NewElement("pngdec")
	if err != nil {
		fmt.Println("pngdec:", err)
		os.Exit(1)
	}
	freeze, err := gst.NewElement("imagefreeze")
	if err != nil {
		fmt.Println("imagefreeze:", err)
		os.Exit(1)
	}
	conv, err := gst.NewElement("videoconvert")
	if err != nil {
		fmt.Println("videoconvert:", err)
		os.Exit(1)
	}
	scale, err := gst.NewElementWithProperties("videoscale", map[string]interface{}{"add-borders": true})
	if err != nil {
		fmt.Println("videoscale:", err)
		os.Exit(1)
	}
	scaleCaps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		fmt.Println("scaleCaps:", err)
		os.Exit(1)
	}
	rate, err := gst.NewElementWithProperties("videorate", map[string]interface{}{"drop-only": true})
	if err != nil {
		fmt.Println("videorate:", err)
		os.Exit(1)
	}
	rateCaps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,framerate=5/1"),
	})
	if err != nil {
		fmt.Println("rateCaps:", err)
		os.Exit(1)
	}
	q, err := gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(3),
		"leaky":            int(2),
	})
	if err != nil {
		fmt.Println("queue:", err)
		os.Exit(1)
	}
	enc, err := gst.NewElementWithProperties("vp8enc", map[string]interface{}{
		"deadline":          int(1),
		"target-bitrate":    int(500_000),
		"cpu-used":          int(8),
		"keyframe-max-dist": int(5),
		"end-usage":         int(1),
	})
	if err != nil {
		fmt.Println("vp8enc:", err)
		os.Exit(1)
	}
	tailCaps, err := gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-vp8"),
	})
	if err != nil {
		fmt.Println("tailCaps:", err)
		os.Exit(1)
	}
	sink, err := gst.NewElementWithProperties("fakesink", map[string]interface{}{
		"sync":            false,
		"signal-handoffs": true,
	})
	if err != nil {
		fmt.Println("fakesink:", err)
		os.Exit(1)
	}

	if err := pipeline.AddMany(src, tf, dec, freeze, conv, scale, scaleCaps, rate, rateCaps, q, enc, tailCaps, sink); err != nil {
		fmt.Println("addmany:", err)
		os.Exit(1)
	}
	if err := gst.ElementLinkMany(src, tf, dec, freeze, conv, scale, scaleCaps, rate, rateCaps, q, enc, tailCaps, sink); err != nil {
		fmt.Println("link:", err)
		os.Exit(1)
	}

	var frames atomic.Int64
	sink.Connect("handoff", func() {
		frames.Add(1)
	})

	bus := pipeline.GetBus()
	bus.AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageError:
			fmt.Println("BUS ERROR:", msg.ParseError())
		case gst.MessageWarning:
			fmt.Println("BUS WARNING:", msg.ParseWarning())
		}
		return true
	})

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		fmt.Println("set playing err:", err)
		os.Exit(1)
	}
	time.Sleep(3 * time.Second)
	fmt.Printf("frames produced: %d\n", frames.Load())
	pipeline.SetState(gst.StateNull)
	if frames.Load() == 0 {
		os.Exit(2)
	}
}
