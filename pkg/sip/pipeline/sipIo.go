package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/hop"
	"github.com/samber/lo"
)

func NewSipInput(log logger.Logger, parent *Pipeline, opts SipOpt) *SipIo {
	return &SipIo{
		log:         log.WithComponent("sip_input"),
		pipeline:    parent,
		opts:        opts,
		sendOfferCh: make(chan string, 1),
		hops:        make(map[string]*Hop),
	}
}

type SipOpt struct {
	IP                    string
	PortStart             uint16
	PortEnd               uint16
	VideoWidth            uint
	VideoHeight           uint
	Framerate             uint
	MaxActiveParticipants int
	Gst                   config.GstConfig
	PublishCodecs         config.PublishCodecConfig
}

type Hop struct {
	srcPad  *gst.Pad
	src     *gst.Element
	sink    *gst.Element
	sinkPad *gst.Pad
}

type SipIo struct {
	log      logger.Logger
	pipeline *Pipeline

	opts SipOpt

	SipBin      *gst.Element
	sendOfferCh chan string

	hopMu sync.Mutex
	hops  map[string]*Hop
}

func makeH264HighCaps() *gst.Caps {
	profiles := []string{
		// High (unconstrained) — most common modern deployment
		"640029", // High 4.1 — typical 1080p30 (IDEAL first value)
		"64002a", // High 4.2 — 1080p60
		"640028", // High 4.0
		"640020", // High 3.2 — Poly/Tandberg (was missing!)
		"64001f", // High 3.1 — 720p
		"64001e", // High 3.0
		"640032", // High 5.0 — 4k-ish
		"640033", // High 5.1 — 4k60
		"640034", // High 5.2 — 4k120
		// Constrained High (constraint_set4+5: progressive, no B-frames)
		"640c29", "640c2a", "640c28", "640c20", "640c1f", "640c1e",
		// Progressive High (constraint_set4 only: progressive, B-frames OK)
		"640829", "64082a", "640828", "640820", "64081f", "64081e",
	}

	profiles = lo.Interleave(profiles, lo.Map(profiles, func(p string, _ int) string { return strings.ToUpper(p) }))
	profiles = lo.Map(profiles, func(p string, _ int) string { return "(string)" + p })

	return gst.NewCapsFromString(fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,packetization-mode=(string)1,profile-level-id={%s}",
		strings.Join(profiles, ",")))
}

func makeH264MainCaps() *gst.Caps {
	profiles := []string{
		// Main (unconstrained)
		"4d0029", "4d002a", "4d0028", "4d0020", "4d001f", "4d001e",
		// Main with constraint_set1 (Main-compat signaling, seen on Cisco)
		"4d4029", "4d4028", "4d401f",
	}

	profiles = lo.Interleave(profiles, lo.Map(profiles, func(p string, _ int) string { return strings.ToUpper(p) }))
	profiles = lo.Map(profiles, func(p string, _ int) string { return "(string)" + p })

	return gst.NewCapsFromString(fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,packetization-mode=(string)1,profile-level-id={%s}",
		strings.Join(profiles, ",")))
}

func makeH264BaselineCaps() *gst.Caps {
	profiles := []string{
		// --- Constrained Baseline (constraint_set0+1+2) --- WebRTC/Teams/modern
		"42e01f", // CBP 3.1 — THE universal WebRTC/Teams default
		"42e029", // CBP 4.1 — HD CBP
		"42e028", // CBP 4.0
		"42e02a", // CBP 4.2
		"42e020", // CBP 3.2
		"42e01e", // CBP 3.0
		"42e016", // CBP 2.2
		"42e015", // CBP 2.1 — older phones
		"42e014", // CBP 2.0
		"42e00d", // CBP 1.3 — legacy / IoT
		"42e00c", // CBP 1.2
		"42e00b", // CBP 1.1
		"42e00a", // CBP 1.0
		// --- Poly-style Baseline (constraint_set0 only) ---
		// Polycom Group Series, Trio, some RealPresence
		"42801f", "428020", "428028", "428029", "42802a",
		"42801e", "428015", "42800d", "42800a",
		// --- "Main-compatible" Baseline (constraint_set0+1) ---
		// Some Cisco Telepresence, older Tandberg
		"42c01f", "42c028", "42c029", "42c02a", "42c020", "42c01e",
		// --- Pure Baseline (no constraints) ---
		// Some Asterisk, FreeSWITCH, old IP phones
		"42001f", "420028", "420029", "42002a", "420020", "42001e",
		"420015", "42000d", "42000b", "42000a",
		// --- Baseline Level 1b (constraint_set3 set) ---
		// Some legacy / mobile / 3G SIP phones
		"42100b", "42900b", "42d00b", "42f00b",
	}

	profiles = lo.Interleave(profiles, lo.Map(profiles, func(p string, _ int) string { return strings.ToUpper(p) }))
	profiles = lo.Map(profiles, func(p string, _ int) string { return "(string)" + p })

	return gst.NewCapsFromString(fmt.Sprintf(
		"application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,packetization-mode=(string)1,profile-level-id={%s}",
		strings.Join(profiles, ",")))
}

var _ GstChain = (*SipIo)(nil)

// Create implements [GstChain].
func (sio *SipIo) Create() error {
	var err error

	formatCaps := []*gst.Caps{
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=G722,clock-rate=8000"),
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMU,clock-rate=8000"),
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMA,clock-rate=8000"),
		gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=TELEPHONE-EVENT,clock-rate=8000"),
		makeH264HighCaps(),
		makeH264MainCaps(),
		makeH264BaselineCaps(),
		gst.NewCapsFromString("application/x-rtp,media=video,packetization-mode=(string)1,encoding-name=H264,clock-rate=90000"),
		gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000"),
	}

	formats := lo.Map(formatCaps, func(caps *gst.Caps, _ int) interface{} {
		return caps
	})

	arr, err := glib.NewArray(formats)
	if err != nil {
		return fmt.Errorf("failed to create formats array: %w", err)
	}

	sio.SipBin, err = gst.NewElementWithProperties("sipbin", map[string]interface{}{
		"ip":         sio.opts.IP,
		"port-start": uint(sio.opts.PortStart),
		"port-end":   uint(sio.opts.PortEnd),
		"formats":    arr,
	})
	if err != nil {
		return fmt.Errorf("failed to create SIP sipbin: %w", err)
	}

	return nil
}

// Add implements [GstChain].
func (sio *SipIo) Add() error {
	return sio.pipeline.Pipeline().AddMany(
		sio.SipBin,
	)
}

func (sio *SipIo) binPadAddedRecvRtpSrc(_ *gst.Element, pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}

	var session, ssrc, pt uint
	if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		sio.log.Warnw("Failed to parse recv RTP src pad name", err, "padName", padName)
		return
	}

	sio.log.Debugw("Received new recv RTP src pad on rtpbin", "session", session, "ssrc", ssrc, "pt", pt)

	sio.hopMu.Lock()
	defer sio.hopMu.Unlock()

	sinkPad := sio.pipeline.IOManager.SipController.GetRequestPad(fmt.Sprintf("recv_rtp_sink_%d_%d_%d", session, ssrc, pt))
	if sinkPad == nil {
		sio.log.Warnw("Received new recv RTP src pad on rtpbin, but no matching sink pad was found on sipbin", nil, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	hopSink, hopSrc, err := hop.NewPair()
	if err != nil {
		sio.log.Errorw("Failed to create hop pair for new recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if err := sio.pipeline.Pipeline().AddMany(
		hopSrc, hopSink,
	); err != nil {
		sio.log.Errorw("Failed to add hop elements to pipeline for new recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if ret := hopSrc.GetStaticPad("src").Link(sinkPad); ret != gst.PadLinkOK {
		sio.log.Errorw("Failed to link new hop src pad to sipbin sink pad", fmt.Errorf("link failed: %v", ret), "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if ret := pad.Link(hopSink.GetStaticPad("sink")); ret != gst.PadLinkOK {
		sio.log.Errorw("Failed to link new recv RTP src pad from rtpbin to sipbin sink pad", fmt.Errorf("link failed: %v", ret), "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if !hopSrc.SyncStateWithParent() {
		sio.log.Warnw("Failed to sync hop src state with parent pipeline for new recv RTP pad", nil, "session", session, "ssrc", ssrc, "pt", pt)
	}
	if !hopSink.SyncStateWithParent() {
		sio.log.Warnw("Failed to sync hop sink state with parent pipeline for new recv RTP pad", nil, "session", session, "ssrc", ssrc, "pt", pt)
	}

	sio.hops[fmt.Sprintf("%d_%d_%d", session, ssrc, pt)] = &Hop{
		srcPad:  pad,
		src:     hopSrc,
		sink:    hopSink,
		sinkPad: sinkPad,
	}

	sio.log.Infow("Linked new recv RTP src pad from rtpbin to sipbin sink pad", "session", session, "ssrc", ssrc, "pt", pt)
}

func (sio *SipIo) binPadRemoved(_ *gst.Element, pad *gst.Pad) {
	padName := pad.GetName()
	if !strings.HasPrefix(padName, "recv_rtp_src_") {
		return
	}

	var session, ssrc, pt uint
	if _, err := fmt.Sscanf(padName, "recv_rtp_src_%d_%d_%d", &session, &ssrc, &pt); err != nil {
		sio.log.Warnw("Failed to parse removed recv RTP src pad name", err, "padName", padName)
		return
	}

	sio.log.Debugw("Received removed recv RTP src pad on rtpbin", "session", session, "ssrc", ssrc, "pt", pt)

	sio.hopMu.Lock()
	defer sio.hopMu.Unlock()

	hopKey := fmt.Sprintf("%d_%d_%d", session, ssrc, pt)
	hop, ok := sio.hops[hopKey]
	if !ok {
		sio.log.Warnw("Received removed recv RTP src pad on rtpbin, but no matching hop was found", nil, "session", session, "ssrc", ssrc, "pt", pt)
		return
	}

	if hop.sinkPad.GetParent().Instance() != nil {
		sio.pipeline.IOManager.SipController.ReleaseRequestPad(hop.sinkPad)
	}

	for _, elem := range []*gst.Element{hop.sink, hop.src} {
		if err := elem.SetState(gst.StateNull); err != nil {
			sio.log.Errorw("Failed to set hop element to NULL state for removed recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		}
		if err := sio.pipeline.Pipeline().Remove(elem); err != nil {
			sio.log.Errorw("Failed to remove hop element from pipeline for removed recv RTP pad", err, "session", session, "ssrc", ssrc, "pt", pt)
		}
	}
	sio.hops[hopKey] = nil
	delete(sio.hops, hopKey)

	sio.log.Infow("Unlinked and removed hop for removed recv RTP src pad on rtpbin", "session", session, "ssrc", ssrc, "pt", pt)
}

func (sio *SipIo) onAvailableMedia(camera, microphone, screenshare, screenshareAudio bool) {
	screenshareAudio = screenshareAudio || (microphone && screenshare)

	sio.log.Infow("Available media", "camera", camera, "microphone", microphone, "screenshare", screenshare, "screenshareAudio", screenshareAudio)

	if err := errors.Join(
		sio.pipeline.WebrtcIo.LivekitBin.SetProperty("microphone", microphone),
		sio.pipeline.WebrtcIo.LivekitBin.SetProperty("camera", camera),
		sio.pipeline.WebrtcIo.LivekitBin.SetProperty("screenshare", screenshare),
		sio.pipeline.WebrtcIo.LivekitBin.SetProperty("screenshare-audio", screenshareAudio),
	); err != nil {
		sio.log.Errorw("Failed to set available media properties on LiveKit bin", err, "camera", camera, "microphone", microphone, "screenshare", screenshare, "screenshareAudio", screenshareAudio)
	}
}

// Link implements [GstChain].
func (sio *SipIo) Link() error {
	// link rtp in
	siow := weak.Make(sio)

	if _, err := sio.SipBin.Connect("pad-added", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.binPadAddedRecvRtpSrc(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-added signal: %w", err)
	}

	if _, err := sio.SipBin.Connect("pad-removed", func(rtpbin *gst.Element, pad *gst.Pad) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.binPadRemoved(rtpbin, pad)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect to rtpbin pad-removed signal: %w", err)
	}

	if _, err := sio.SipBin.Connect("send-offer-sdp", func(_ *gst.Element, offer string) {
		ptr := siow.Value()
		if ptr != nil {
			select {
			case ptr.sendOfferCh <- offer:
			default:
				ptr.log.Warnw("send-offer-sdp channel full, dropping offer", nil)
			}
		}
	}); err != nil {
		return fmt.Errorf("failed to connect send-offer-sdp signal: %w", err)
	}

	if _, err := sio.SipBin.Connect("available-media", func(_ *gst.Element, camera, microphone, screenshare, screenshareAudio bool) {
		ptr := siow.Value()
		if ptr != nil {
			ptr.onAvailableMedia(camera, microphone, screenshare, screenshareAudio)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect available-media signal: %w", err)
	}

	return nil
}

// Close implements [GstChain].
func (sio *SipIo) Close() error {
	if err := sio.pipeline.Pipeline().RemoveMany(
		sio.SipBin,
	); err != nil {
		return fmt.Errorf("failed to remove SIP IO elements from pipeline: %w", err)
	}
	sio.SipBin = nil
	sio.hopMu.Lock()
	defer sio.hopMu.Unlock()
	for _, hop := range sio.hops {
		if err := sio.pipeline.Pipeline().RemoveMany(
			hop.src, hop.sink,
		); err != nil {
			sio.log.Errorw("Failed to remove hop elements from pipeline", err)
		}
	}
	sio.hops = make(map[string]*Hop)
	return nil
}
