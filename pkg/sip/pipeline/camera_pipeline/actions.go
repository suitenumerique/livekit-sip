package camera_pipeline

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime/cgo"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline"
	"github.com/livekit/sip/pkg/sip/pipeline/event"
)

func (cp *CameraPipeline) checkReady() error {

	if cp.Pipeline().GetCurrentState() != gst.StatePaused {
		// Already playing
		return nil
	}

	checkHandle := func(elem *gst.Element) bool {
		hasHandleVal, err := elem.GetProperty("has-handle")
		if err != nil {
			return false
		}
		hasHandle, ok := hasHandleVal.(bool)
		return ok && hasHandle
	}

	ready := true
	ready = ready && checkHandle(cp.SipRtpIn)
	ready = ready && checkHandle(cp.SipRtpOut)
	ready = ready && checkHandle(cp.SipRtcpIn)
	ready = ready && checkHandle(cp.SipRtcpOut)
	ready = ready && checkHandle(cp.WebrtcRtpOut)
	ready = ready && checkHandle(cp.WebrtcRtcpOut)

	if ready {
		cp.Log().Infow("All handles ready, setting pipeline to PLAYING")
		if err := cp.SetState(gst.StatePlaying); err != nil {
			return fmt.Errorf("failed to set camera pipeline to playing: %w", err)
		}

		for _, e := range []*gst.Element{
			cp.SipRtpIn,
			cp.SipRtpOut,
			cp.SipRtcpIn,
			cp.SipRtcpOut,
			cp.WebrtcRtpOut,
			cp.WebrtcRtcpOut,
		} {
			if !e.SyncStateWithParent() {
				return fmt.Errorf("failed to sync state with parent for element %s", e.GetName())
			}
		}
	}
	return nil
}

func (cp *CameraPipeline) SipIO(rtp, rtcp net.Conn, pt uint8) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHandle := cgo.NewHandle(rtcp)
	defer rtcpHandle.Delete()

	h264Caps := fmt.Sprintf("application/x-rtp,media=video,encoding-name=H264,payload=%d,clock-rate=90000", pt)

	cp.Log().Infow("Setting SIP IO",
		"rtp_remote", rtp.RemoteAddr(),
		"rtp_local", rtp.LocalAddr(),
		"rtcp_remote", rtcp.RemoteAddr(),
		"rtcp_local", rtcp.LocalAddr(),
		"caps", h264Caps,
	)

	if _, err := cp.SipRtpBin.Connect("request-pt-map", event.RegisterCallback(context.TODO(), cp.loop, func(self *gst.Element, session uint, sipPt uint) *gst.Caps {
		if sipPt == uint(pt) {
			return gst.NewCapsFromString(h264Caps)
		}
		return nil
	})); err != nil {
		return fmt.Errorf("failed to connect to rtpbin request-pt-map signal: %w", err)
	}

	if err := cp.SipRtpIn.SetProperty("caps",
		gst.NewCapsFromString(h264Caps),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp in caps (pt: %d): %w", pt, err)
	}

	if err := cp.SipRtpOut.SetProperty("caps",
		gst.NewCapsFromString(h264Caps),
	); err != nil {
		return fmt.Errorf("failed to set sip rtp out caps (pt: %d): %w", pt, err)
	}

	if err := cp.SipRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set rtp in handle: %w", err)
	}

	if err := cp.SipRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set rtp out handle: %w", err)
	}

	if err := cp.SipRtcpIn.SetProperty("handle", uint64(rtcpHandle)); err != nil {
		return fmt.Errorf("failed to set rtcp in handle: %w", err)
	}

	if err := cp.SipRtcpOut.SetProperty("handle", uint64(rtcpHandle)); err != nil {
		return fmt.Errorf("failed to set rtcp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) WebrtcOutput(rtp, rtcp io.WriteCloser) error {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHnd := cgo.NewHandle(rtcp)
	defer rtcpHnd.Delete()

	if err := cp.WebrtcRtpOut.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return fmt.Errorf("failed to set webrtc rtp out handle: %w", err)
	}

	if err := cp.WebrtcRtcpOut.SetProperty("handle", uint64(rtcpHnd)); err != nil {
		return fmt.Errorf("failed to set webrtc rtcp out handle: %w", err)
	}

	cp.checkReady()

	return nil
}

func (cp *CameraPipeline) AddWebrtcTrack(ssrc uint32, rtp, rtcp io.ReadCloser) (*WebrtcTrack, error) {
	rtpHandle := cgo.NewHandle(rtp)
	defer rtpHandle.Delete()
	rtcpHnd := cgo.NewHandle(rtcp)
	defer rtcpHnd.Delete()

	track, err := pipeline.AddChain(cp, NewWebrtcTrack(cp.Log(), cp.WebrtcIo, ssrc))
	if err != nil {
		return nil, fmt.Errorf("failed to add webrtc track chain: %w", err)
	}

	cp.WebrtcIo.Tracks[ssrc] = track

	if err := track.WebrtcRtpIn.SetProperty("handle", uint64(rtpHandle)); err != nil {
		return nil, fmt.Errorf("failed to set webrtc rtp in handle: %w", err)
	}

	if err := track.WebrtcRtcpIn.SetProperty("handle", uint64(rtcpHnd)); err != nil {
		return nil, fmt.Errorf("failed to set webrtc rtcp in handle: %w", err)
	}

	if err := pipeline.LinkChains(cp, track); err != nil {
		return nil, fmt.Errorf("failed to link webrtc track chain: %w", err)
	}

	// if err := cp.DirtySwitchWebrtcInput(ssrc); err != nil {
	// 	return nil, fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", ssrc, err)
	// }

	return track, nil
}

func (cp *CameraPipeline) RemoveWebrtcTrack(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if err := track.Close(); err != nil {
		return fmt.Errorf("failed to close webrtc track with ssrc %d: %w", ssrc, err)
	}

	var newTrack *WebrtcTrack
	for s, t := range cp.WebrtcIo.Tracks {
		if s != ssrc {
			newTrack = t
			break
		}
	}

	if newTrack != nil {
		if err := cp.DirtySwitchWebrtcInput(newTrack.SSRC); err != nil {
			return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", newTrack.SSRC, err)
		}
	}

	return nil
}

func (cp *CameraPipeline) DirtySwitchWebrtcInput(ssrc uint32) error {
	track, ok := cp.WebrtcIo.Tracks[ssrc]
	if !ok {
		return fmt.Errorf("webrtc track with ssrc %d not found", ssrc)
	}

	if err := cp.WebrtcIo.InputSelector.SetProperty("active-pad",
		track.RtpQueue.GetStaticPad("src"),
	); err != nil {
		return fmt.Errorf("failed to switch webrtc input to ssrc %d: %w", ssrc, err)
	}

	return nil
}
