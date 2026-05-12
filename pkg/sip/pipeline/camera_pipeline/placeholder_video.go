package camera_pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

// PlaceholderImagePath is where the SIP server writes the embedded
// encryption-blocked PNG at startup.
var PlaceholderImagePath = filepath.Join(os.TempDir(), "encryption_blocked.png")

// PlaceholderVideo produces a frozen-frame *raw* video stream from a PNG
// and inserts a `RawSelector` (input-selector at the raw-YUV layer)
// between the bridge chain's ScaleFilter and Queue elements. The selector
// picks between:
//
//	sink_bridge      ← the existing webrtc→sip raw-YUV stream (Vp8Dec out)
//	sink_placeholder ← this chain's raw PNG video
//
// Switching at the raw-video layer (not at encoded VP8) means no decoder
// state to desync: x264enc sees different YUV frames and produces a brief
// glitch that Janus's H.264 decoder absorbs. No PLIs, no IDR coordination,
// no `DISCONT` flagging needed.
//
// Both paths produce video/x-raw,format=I420,width=1280,height=720
// with framerate=24/1 (matching the bridge chain's RateFilter caps).
type PlaceholderVideo struct {
	pipeline *CameraPipeline
	log      logger.Logger

	Source      *gst.Element // filesrc — one-shot read of the PNG bytes
	TypeFind    *gst.Element // typefind force-caps=image/png (avoids preroll deadlock)
	PngDec      *gst.Element // pngdec
	ImgFreeze   *gst.Element // imagefreeze — emits the same frame forever
	Convert     *gst.Element // videoconvert — RGBA → I420
	Scale       *gst.Element // videoscale → 1280×720 with letterboxing
	ScaleCaps   *gst.Element // capsfilter: I420 1280×720
	Rate        *gst.Element // videorate — pace to 24/1 to match bridge
	RateCaps    *gst.Element // capsfilter: framerate=24/1
	Queue       *gst.Element // queue — decouple from downstream backpressure

	RawSelector    *gst.Element // input-selector at raw-YUV layer
	bridgePad      *gst.Pad     // RawSelector sink fed by the bridge chain
	placeholderPad *gst.Pad     // RawSelector sink fed by this chain

	enabled atomic.Bool
}

var _ pipeline.GstChain = (*PlaceholderVideo)(nil)

func NewPlaceholderVideo(log logger.Logger, parent *CameraPipeline) *PlaceholderVideo {
	return &PlaceholderVideo{
		pipeline: parent,
		log:      log.WithComponent("placeholder_video"),
	}
}

// SetLocation is a no-op; the PNG path is fixed at Create() time.
func (pv *PlaceholderVideo) SetLocation(path string) error {
	_ = path
	return nil
}

func (pv *PlaceholderVideo) Create() error {
	var err error

	pv.Source, err = gst.NewElementWithProperties("filesrc", map[string]interface{}{
		"location": PlaceholderImagePath,
	})
	if err != nil {
		return fmt.Errorf("placeholder filesrc: %w", err)
	}

	pv.TypeFind, err = gst.NewElementWithProperties("typefind", map[string]interface{}{
		"force-caps": gst.NewCapsFromString("image/png"),
	})
	if err != nil {
		return fmt.Errorf("placeholder typefind: %w", err)
	}

	pv.PngDec, err = gst.NewElement("pngdec")
	if err != nil {
		return fmt.Errorf("placeholder pngdec: %w", err)
	}

	pv.ImgFreeze, err = gst.NewElement("imagefreeze")
	if err != nil {
		return fmt.Errorf("placeholder imagefreeze: %w", err)
	}

	pv.Convert, err = gst.NewElement("videoconvert")
	if err != nil {
		return fmt.Errorf("placeholder videoconvert: %w", err)
	}

	pv.Scale, err = gst.NewElementWithProperties("videoscale", map[string]interface{}{
		"add-borders": true,
	})
	if err != nil {
		return fmt.Errorf("placeholder videoscale: %w", err)
	}

	pv.ScaleCaps, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,pixel-aspect-ratio=1/1"),
	})
	if err != nil {
		return fmt.Errorf("placeholder scaleCaps: %w", err)
	}

	pv.Rate, err = gst.NewElementWithProperties("videorate", map[string]interface{}{
		"drop-only": false, // duplicate frames if needed to hit 24/1
	})
	if err != nil {
		return fmt.Errorf("placeholder videorate: %w", err)
	}

	pv.RateCaps, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("placeholder rateCaps: %w", err)
	}

	pv.Queue, err = gst.NewElementWithProperties("queue", map[string]interface{}{
		"max-size-buffers": uint(3),
		"leaky":            int(2),
	})
	if err != nil {
		return fmt.Errorf("placeholder queue: %w", err)
	}

	pv.RawSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{
		"name":          "placeholder_raw_selector",
		"cache-buffers": false,
		"sync-streams":  false, // each sink is independent, no need to sync
	})
	if err != nil {
		return fmt.Errorf("placeholder raw selector: %w", err)
	}

	return nil
}

func (pv *PlaceholderVideo) Add() error {
	return pv.pipeline.Pipeline().AddMany(
		pv.Source,
		pv.TypeFind,
		pv.PngDec,
		pv.ImgFreeze,
		pv.Convert,
		pv.Scale,
		pv.ScaleCaps,
		pv.Rate,
		pv.RateCaps,
		pv.Queue,
		pv.RawSelector,
	)
}

func (pv *PlaceholderVideo) Link() error {
	// 1. Internal chain: PNG → raw I420 1280×720 24/1.
	if err := gst.ElementLinkMany(
		pv.Source,
		pv.TypeFind,
		pv.PngDec,
		pv.ImgFreeze,
		pv.Convert,
		pv.Scale,
		pv.ScaleCaps,
		pv.Rate,
		pv.RateCaps,
		pv.Queue,
	); err != nil {
		return fmt.Errorf("link placeholder chain: %w", err)
	}

	if pv.pipeline.WebrtcToSip == nil {
		return fmt.Errorf("WebrtcToSip not initialised yet")
	}
	// Splice into the existing webrtc→sip chain between the post-scale
	// Queue and the X264Enc encoder. Both feeds at this point are I420
	// 1280×720 24/1 raw video.
	bridgeSrc := pv.pipeline.WebrtcToSip.Queue.GetStaticPad("src")
	x264Sink := pv.pipeline.WebrtcToSip.X264Enc.GetStaticPad("sink")
	if bridgeSrc == nil || x264Sink == nil {
		return fmt.Errorf("missing bridge Queue.src or X264Enc.sink")
	}
	if peer := bridgeSrc.GetPeer(); peer != nil {
		bridgeSrc.Unlink(peer)
	}

	// 2. RawSelector sinks
	pv.bridgePad = pv.RawSelector.GetRequestPad("sink_%u")
	if pv.bridgePad == nil {
		return fmt.Errorf("RawSelector bridge pad")
	}
	if err := pipeline.LinkPad(bridgeSrc, pv.bridgePad); err != nil {
		return fmt.Errorf("link bridge → RawSelector: %w", err)
	}

	pv.placeholderPad = pv.RawSelector.GetRequestPad("sink_%u")
	if pv.placeholderPad == nil {
		return fmt.Errorf("RawSelector placeholder pad")
	}
	if err := pipeline.LinkPad(pv.Queue.GetStaticPad("src"), pv.placeholderPad); err != nil {
		return fmt.Errorf("link placeholder → RawSelector: %w", err)
	}

	// 3. RawSelector.src → X264Enc.sink
	if err := pipeline.LinkPad(pv.RawSelector.GetStaticPad("src"), x264Sink); err != nil {
		return fmt.Errorf("link RawSelector → X264Enc: %w", err)
	}

	// 4. Default to the bridge sink so the gateway still works in
	// non-encrypted rooms without any encryption-watcher input.
	if err := pv.RawSelector.SetProperty("active-pad", pv.bridgePad); err != nil {
		return fmt.Errorf("set initial active-pad to bridge: %w", err)
	}
	pv.enabled.Store(false)
	return nil
}

func (pv *PlaceholderVideo) Close() error {
	if pv.RawSelector != nil {
		if pv.bridgePad != nil {
			pv.RawSelector.ReleaseRequestPad(pv.bridgePad)
			pv.bridgePad = nil
		}
		if pv.placeholderPad != nil {
			pv.RawSelector.ReleaseRequestPad(pv.placeholderPad)
			pv.placeholderPad = nil
		}
	}
	return pv.pipeline.Pipeline().RemoveMany(
		pv.Source,
		pv.TypeFind,
		pv.PngDec,
		pv.ImgFreeze,
		pv.Convert,
		pv.Scale,
		pv.ScaleCaps,
		pv.Rate,
		pv.RateCaps,
		pv.Queue,
		pv.RawSelector,
	)
}

// Activate switches the RawSelector active-pad to the placeholder (true)
// or the bridge (false). Idempotent. Safe to call every watcher tick.
// Cheap — just one SetProperty.
func (pv *PlaceholderVideo) Activate(active bool) error {
	if pv.RawSelector == nil || pv.bridgePad == nil || pv.placeholderPad == nil {
		return fmt.Errorf("RawSelector not initialised")
	}
	target := pv.bridgePad
	if active {
		target = pv.placeholderPad
	}
	was := pv.enabled.Load()
	if err := pv.RawSelector.SetProperty("active-pad", target); err != nil {
		return fmt.Errorf("set active-pad: %w", err)
	}
	pv.enabled.Store(active)
	if was != active {
		if active {
			pv.log.Infow("placeholder video activated (raw-layer switch)")
		} else {
			pv.log.Infow("placeholder video deactivated (raw-layer switch, bridge mode)")
		}
	}
	return nil
}
