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

// PlaceholderVideo manages a `RawSelector` (input-selector at the raw-YUV
// layer) inserted between `WebrtcToSip.Queue` and `WebrtcToSip.X264Enc`.
// The selector picks between three independent raw-video sources:
//
//	sink_bridge      ← Vp8Dec output (live participant video)
//	sink_placeholder ← PNG, looped via imagefreeze (encryption-blocked sign)
//	sink_black       ← videotestsrc pattern=black (always-running fallback
//	                   used when bridge has no upstream — e.g. no remote
//	                   browser has its camera enabled)
//
// Switching at the raw-video layer means no codec state to desync, so this
// is safe to toggle as often as needed. The encryption watcher in inbound.go
// drives the choice every tick.
type PlaceholderVideo struct {
	pipeline *CameraPipeline
	log      logger.Logger

	// Placeholder PNG source chain
	Source     *gst.Element
	TypeFind   *gst.Element
	PngDec     *gst.Element
	ImgFreeze  *gst.Element
	Convert    *gst.Element
	Scale      *gst.Element
	ScaleCaps  *gst.Element
	Rate       *gst.Element
	RateCaps   *gst.Element
	Queue      *gst.Element

	// Black source chain
	BlackSource    *gst.Element
	BlackCaps      *gst.Element

	RawSelector    *gst.Element
	bridgePad      *gst.Pad
	placeholderPad *gst.Pad
	blackPad       *gst.Pad

	enabled atomic.Bool // true ⇔ placeholder is the active source
}

var _ pipeline.GstChain = (*PlaceholderVideo)(nil)

// PadKind selects which sink the RawSelector forwards.
type PadKind int

const (
	PadBridge      PadKind = iota // live webrtc video
	PadPlaceholder                // encryption-blocked PNG
	PadBlack                      // black fallback (no bridge video)
)

func NewPlaceholderVideo(log logger.Logger, parent *CameraPipeline) *PlaceholderVideo {
	return &PlaceholderVideo{
		pipeline: parent,
		log:      log.WithComponent("placeholder_video"),
	}
}

func (pv *PlaceholderVideo) SetLocation(path string) error {
	_ = path
	return nil
}

func (pv *PlaceholderVideo) Create() error {
	var err error

	// --- placeholder PNG chain ----------------------------------------
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
		"drop-only": false,
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

	// --- black source chain ------------------------------------------
	pv.BlackSource, err = gst.NewElementWithProperties("videotestsrc", map[string]interface{}{
		"pattern":  int(2), // black (GST_VIDEO_TEST_SRC_BLACK)
		"is-live":  true,
	})
	if err != nil {
		return fmt.Errorf("placeholder black videotestsrc: %w", err)
	}
	pv.BlackCaps, err = gst.NewElementWithProperties("capsfilter", map[string]interface{}{
		"caps": gst.NewCapsFromString("video/x-raw,format=I420,width=1280,height=720,framerate=24/1"),
	})
	if err != nil {
		return fmt.Errorf("placeholder black capsfilter: %w", err)
	}

	pv.RawSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{
		"name":          "placeholder_raw_selector",
		"cache-buffers": false,
		"sync-streams":  false,
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
		pv.BlackSource,
		pv.BlackCaps,
		pv.RawSelector,
	)
}

func (pv *PlaceholderVideo) Link() error {
	// 1. Placeholder PNG chain.
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

	// 2. Black source chain.
	if err := gst.ElementLinkMany(pv.BlackSource, pv.BlackCaps); err != nil {
		return fmt.Errorf("link black chain: %w", err)
	}

	if pv.pipeline.WebrtcToSip == nil {
		return fmt.Errorf("WebrtcToSip not initialised yet")
	}

	// 3. Splice the bridge: WebrtcToSip.Queue.src → … → X264Enc.sink
	// becomes WebrtcToSip.Queue.src → RawSelector(sink_bridge),
	// RawSelector.src → X264Enc.sink.
	bridgeSrc := pv.pipeline.WebrtcToSip.Queue.GetStaticPad("src")
	x264Sink := pv.pipeline.WebrtcToSip.X264Enc.GetStaticPad("sink")
	if bridgeSrc == nil || x264Sink == nil {
		return fmt.Errorf("missing bridge Queue.src or X264Enc.sink")
	}
	if peer := bridgeSrc.GetPeer(); peer != nil {
		bridgeSrc.Unlink(peer)
	}

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

	pv.blackPad = pv.RawSelector.GetRequestPad("sink_%u")
	if pv.blackPad == nil {
		return fmt.Errorf("RawSelector black pad")
	}
	if err := pipeline.LinkPad(pv.BlackCaps.GetStaticPad("src"), pv.blackPad); err != nil {
		return fmt.Errorf("link black → RawSelector: %w", err)
	}

	if err := pipeline.LinkPad(pv.RawSelector.GetStaticPad("src"), x264Sink); err != nil {
		return fmt.Errorf("link RawSelector → X264Enc: %w", err)
	}

	// 4. Default to the bridge pad (matches pre-placeholder behavior).
	if err := pv.RawSelector.SetProperty("active-pad", pv.bridgePad); err != nil {
		return fmt.Errorf("set initial active-pad to bridge: %w", err)
	}
	pv.enabled.Store(false)
	return nil
}

func (pv *PlaceholderVideo) Close() error {
	if pv.RawSelector != nil {
		for _, p := range []**gst.Pad{&pv.bridgePad, &pv.placeholderPad, &pv.blackPad} {
			if *p != nil {
				pv.RawSelector.ReleaseRequestPad(*p)
				*p = nil
			}
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
		pv.BlackSource,
		pv.BlackCaps,
		pv.RawSelector,
	)
}

// SelectPad routes the RawSelector to one of the three sinks. Cheap +
// idempotent; safe to call every watcher tick.
func (pv *PlaceholderVideo) SelectPad(kind PadKind) error {
	var target *gst.Pad
	switch kind {
	case PadPlaceholder:
		target = pv.placeholderPad
	case PadBlack:
		target = pv.blackPad
	default:
		target = pv.bridgePad
	}
	if target == nil {
		return fmt.Errorf("target pad not allocated")
	}
	was := pv.enabled.Load()
	if err := pv.RawSelector.SetProperty("active-pad", target); err != nil {
		return fmt.Errorf("set active-pad: %w", err)
	}
	nowOn := kind == PadPlaceholder
	pv.enabled.Store(nowOn)
	if was != nowOn {
		switch kind {
		case PadPlaceholder:
			pv.log.Infow("RawSelector: placeholder PNG")
		case PadBlack:
			pv.log.Infow("RawSelector: black fallback")
		default:
			pv.log.Infow("RawSelector: live bridge")
		}
	}
	return nil
}

// Activate is the legacy two-state API kept for compatibility. Prefer
// SelectPad in new call sites — it knows about the black fallback.
func (pv *PlaceholderVideo) Activate(active bool) error {
	if active {
		return pv.SelectPad(PadPlaceholder)
	}
	return pv.SelectPad(PadBridge)
}
