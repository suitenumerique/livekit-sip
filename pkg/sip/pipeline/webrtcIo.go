package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"weak"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/hop"
)

func NewWebrtcIo(log logger.Logger, parent *Pipeline) *WebrtcIo {
	return &WebrtcIo{
		log:      log.WithComponent("webrtc_io"),
		pipeline: parent,
		hops:     make(map[string]*Hop),
	}
}

type WebrtcIo struct {
	pipeline *Pipeline
	log      logger.Logger

	connected core.Fuse
	closed    core.Fuse

	LivekitBin *gst.Element

	hopMu sync.Mutex
	hops  map[string]*Hop
}

var _ GstChain = (*WebrtcIo)(nil)

// Create implements [GstChain].
func (wio *WebrtcIo) Create() error {
	var err error

	wio.log.Infow("Creating WebRTC IO", "maxActiveParticipants", wio.pipeline.maxActiveParticipants)

	props := make(map[string]interface{})
	if wio.pipeline.maxActiveParticipants > 0 {
		props["max-active-participants"] = uint(wio.pipeline.maxActiveParticipants)
	}
	if wio.pipeline.publishCoders.Camera != "" {
		props["camera-mime-type"] = wio.pipeline.publishCoders.Camera
	}
	if wio.pipeline.publishCoders.Microphone != "" {
		props["microphone-mime-type"] = wio.pipeline.publishCoders.Microphone
	}
	if wio.pipeline.publishCoders.Screenshare != "" {
		props["screenshare-mime-type"] = wio.pipeline.publishCoders.Screenshare
	}
	if wio.pipeline.publishCoders.ScreenshareAudio != "" {
		props["screenshare-audio-mime-type"] = wio.pipeline.publishCoders.ScreenshareAudio
	}

	wio.LivekitBin, err = gst.NewElementWithProperties("livekitbin", props)
	if err != nil {
		return fmt.Errorf("failed to create livekitbin: %w", err)
	}

	wiow := weak.Make(wio)
	if _, err := wio.LivekitBin.Connect("connected", func(_ *gst.Element) {
		ptr := wiow.Value()
		if ptr != nil {
			ptr.connected.Break()
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to livekitbin connected signal: %w", err)
	}

	if _, err := wio.LivekitBin.Connect("closed", func(_ *gst.Element) {
		ptr := wiow.Value()
		if ptr != nil {
			ptr.closed.Break()
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to livekitbin closed signal: %w", err)
	}

	if _, err := wio.LivekitBin.Connect("active-speakers-changed", func(_ *gst.Element, structure *gst.Structure) {
		ptr := wiow.Value()
		if ptr != nil {
			if _, err := ptr.pipeline.LivekitController.Emit("active-speakers-changed", structure); err != nil {
				ptr.log.Errorw("Failed to emit active-speakers-changed signal from livekitbin to io manager", err)
			}
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to livekitbin active-speakers-changed signal: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (wio *WebrtcIo) Add() error {
	if err := wio.pipeline.Pipeline().AddMany(
		wio.LivekitBin,
	); err != nil {
		return fmt.Errorf("failed to add webrtc io to pipeline: %w", err)
	}
	return nil
}

func (wio *WebrtcIo) binPadAdded(_ *gst.Element, pad *gst.Pad) {
	wio.log.Debugw("RTP bin pad added", "pad", pad.GetName())
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}

	var session, ssrc, pt uint
	if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		wio.log.Warnw("Failed to parse recv RTP src pad name", err, "pad", padName)
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_CAMERA:
	case livekit.TrackSource_MICROPHONE:
	case livekit.TrackSource_SCREEN_SHARE:
	case livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		wio.log.Warnw("Unknown track source in RTP pad name", nil, "session", session, "pad", padName)
		return
	}

	wio.hopMu.Lock()
	defer wio.hopMu.Unlock()

	sinkPad := wio.pipeline.IOManager.LivekitController.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt))
	if sinkPad == nil {
		wio.log.Errorw("Failed to get request pad from IO Manager for new recv RTP pad", nil, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	hopSink, hopSrc, err := hop.NewPair()
	if err != nil {
		wio.log.Errorw("Failed to create hop pair for new recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if err := wio.pipeline.Pipeline().AddMany(
		hopSrc, hopSink,
	); err != nil {
		wio.log.Errorw("Failed to add hop elements to pipeline for new recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if ret := hopSrc.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		wio.log.Errorw("Failed to link new hop src pad to LiveKit IO Manager sink pad", fmt.Errorf("link failed: %v", ret), "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if ret := pad.Link(hopSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		wio.log.Errorw("Failed to link new recv RTP src pad from livekitbin to hop sink pad", fmt.Errorf("link failed: %v", ret), "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if !hopSrc.SyncStateWithParent() {
		wio.log.Warnw("Failed to sync hop src state with parent pipeline for new recv RTP pad", nil, "session", session, "ssrc", ssrc, "pt", pt)
	}
	if !hopSink.SyncStateWithParent() {
		wio.log.Warnw("Failed to sync hop sink state with parent pipeline for new recv RTP pad", nil, "session", session, "ssrc", ssrc, "pt", pt)
	}

	wio.hops[fmt.Sprintf("%d_%d_%d", session, ssrc, pt)] = &Hop{
		srcPad:  pad,
		src:     hopSrc,
		sink:    hopSink,
		sinkPad: sinkPad,
	}

	wio.log.Infow("Linked WebRTC RTP pad to IO Manager", "pad", padName, "session", session, "ssrc", ssrc, "payloadType", pt)
}

func (wio *WebrtcIo) binPadRemoved(_ *gst.Element, pad *gst.Pad) {
	if pad == nil || pad.GetPadTemplate() == nil || pad.GetPadTemplate().GetName() != "recv_rtp_src_%u_%u_%u" {
		return
	}

	var session, ssrc, pt uint
	if _, err := fmt.Sscanf(pad.GetName(), "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		wio.log.Warnw("Received removed pad on rtpbin with unrecognized name format", err, "padName", pad.GetName())
		return
	}

	wio.log.Debugw("Received removed recv RTP src pad on rtpbin", "session", session, "ssrc", ssrc, "pt", pt)

	wio.hopMu.Lock()
	defer wio.hopMu.Unlock()

	hopKey := fmt.Sprintf("%d_%d_%d", session, ssrc, pt)
	hop, ok := wio.hops[hopKey]
	if !ok {
		wio.log.Warnw("Received removed recv RTP src pad on rtpbin, but no matching hop was found", nil, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if hop.sinkPad.GetParent().Instance() != nil {
		wio.log.Infow("Releasing request pad from IO Manager for removed recv RTP pad", "session", session, "ssrc", ssrc, "pt", pt)
		wio.pipeline.IOManager.LivekitController.ReleaseRequestPad(hop.sinkPad)
	}

	for _, elem := range []*gst.Element{hop.sink, hop.src} {
		if err := elem.SetState(gst.StateNull); err != nil {
			wio.log.Errorw("Failed to set hop element to NULL state for removed recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		}
		if err := wio.pipeline.Pipeline().Remove(elem); err != nil {
			wio.log.Errorw("Failed to remove hop element from pipeline for removed recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		}
	}
	wio.hops[hopKey] = nil
	delete(wio.hops, hopKey)

	wio.log.Infow("Unlinked and removed hop for removed recv RTP src pad on rtpbin", "session", session, "ssrc", ssrc, "pt", pt)
}

// Link implements [GstChain].
func (wio *WebrtcIo) Link() error {
	wwio := weak.Make(wio)

	if _, err := wio.LivekitBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadAdded(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-added signal: %w", err)
	}

	if _, err := wio.LivekitBin.Connect("pad-removed", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := wwio.Value()
		if ptr != nil {
			ptr.binPadRemoved(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to webrtc rtpbin pad-removed signal: %w", err)
	}

	return nil
}

func (wio *WebrtcIo) Close() error {
	if err := wio.pipeline.Pipeline().RemoveMany(
		wio.LivekitBin,
	); err != nil {
		return fmt.Errorf("errors occurred while closing webrtc io: %w", err)
	}
	wio.LivekitBin = nil
	wio.hopMu.Lock()
	defer wio.hopMu.Unlock()
	for _, hop := range wio.hops {
		if err := wio.pipeline.Pipeline().RemoveMany(
			hop.src, hop.sink,
		); err != nil {
			wio.log.Errorw("Failed to remove hop elements from pipeline", err)
		}
	}
	wio.hops = make(map[string]*Hop)

	return nil
}

func (wio *WebrtcIo) Connected() <-chan struct{} {
	if wio == nil {
		return nil
	}
	return wio.connected.Watch()
}

func (wio *WebrtcIo) Closed() <-chan struct{} {
	if wio == nil {
		return nil
	}
	return wio.closed.Watch()
}
