package camera_pipeline

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline"
)

func (gp *CameraPipeline) AddWebRTCSourceToSelector(ssrc uint32) (*WebrtcTrack, error) {
	gp.Mu.Lock()
	defer gp.Mu.Unlock()

	if gp.Closed() {
		return nil, fmt.Errorf("pipeline is closed")
	}

	state := gp.Pipeline.GetCurrentState()

	switch state {
	case gst.StatePlaying, gst.StateReady, gst.StatePaused:
	default:
		return nil, fmt.Errorf("pipeline must be in playing, ready, or paused state to add source, current state: %s", state.String())
	}

	if _, exists := gp.WebrtcToSip.WebrtcTracks[ssrc]; exists {
		return nil, fmt.Errorf("webrtc source with ssrc %d already exists", ssrc)
	}

	gp.Log.Debugw("adding WebRTC source to selector", "ssrc", ssrc)
	webRTCToSelector, err := pipeline.CastErr[*WebrtcTrack](gp.AddChain(buildWebrtcTrack(gp.Log.WithComponent("webrtc_to_selector"), gp.WebrtcToSip, ssrc)))
	if err != nil {
		gp.Log.Errorw("Error building WebRTC to selector chain", err)
		return nil, err
	}
	gp.Log.Debugw("built WebRTC to selector chain", "ssrc", ssrc)

	gp.WebrtcToSip.WebrtcTracks[ssrc] = webRTCToSelector

	if state != gst.StatePlaying {
		return webRTCToSelector, nil
	}

	if err := gp.RequestKeyframe(webRTCToSelector); err != nil {
		gp.Log.Warnw("failed to request keyframe from webrtc source after adding to selector", err, "ssrc", ssrc)
	}

	return webRTCToSelector, nil
}

func (gp *CameraPipeline) SwitchWebRTCSelectorSource(ssrc uint32) error {
	gp.Mu.Lock()
	defer gp.Mu.Unlock()
	return gp.switchWebRTCSelectorSource(ssrc)
}

func (gp *CameraPipeline) switchWebRTCSelectorSource(ssrc uint32) error {
	if gp.Closed() {
		return fmt.Errorf("pipeline is closed")
	}

	state := gp.Pipeline.GetCurrentState()
	switch state {
	case gst.StatePlaying:
	default:
		return fmt.Errorf("pipeline must be in playing, paused, or ready state to switch source, current state: %s", state.String())
	}

	track, exists := gp.WebrtcToSip.WebrtcTracks[ssrc]
	if !exists {
		return fmt.Errorf("webrtc source with ssrc %d does not exist", ssrc)
	}

	sel := gp.WebrtcToSip.InputSelector
	selPad := track.RtpSelPad

	activeProp, err := sel.GetProperty("active-pad")
	if err == nil || activeProp != nil {
		activePad, ok := activeProp.(*gst.Pad)
		if !ok || activePad.GetParentElement() == nil {
			return fmt.Errorf("active pad from selector is invalid")
		}

		if activePad.GetName() == selPad.GetName() {
			gp.Log.Debugw("WebRTC selector source is already set to ssrc", "ssrc", ssrc)
			return nil
		}
	}

	return gp.switchSelectorPad(track, selPad)
}

func (gp *CameraPipeline) switchSelectorPad(wt *WebrtcTrack, pad *gst.Pad) error {
	if err := pipeline.ValidatePad(pad); err != nil {
		return fmt.Errorf("invalid pad provided for selector switch: %w", err)
	}

	done := make(chan struct{})
	timeout := time.NewTicker(time.Second * 4)
	defer timeout.Stop()

	var err error

	gp.Log.Debugw("Waiting for keyframe on WebRTC source", "ssrc", wt.SSRC)
	probe := pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			// println("Buffer is nil in pad probe")
			return gst.PadProbePass
		}

		if !buf.HasFlags(gst.BufferFlagDeltaUnit) {
			if err = gp.WebrtcToSip.InputSelector.SetProperty("active-pad", pad); err != nil {
				gp.Log.Errorw("Failed to set active pad on input selector", err)
				err = fmt.Errorf("failed to set active pad on input selector: %w", err)
			}
			close(done)
			// println("Keyframe received on pad probe")
			return gst.PadProbeRemove
		}
		// println("Not a keyframe, continuing to wait...")
		return gst.PadProbePass
	})

	if err := gp.RequestKeyframe(wt); err != nil {
		pad.RemoveProbe(probe)
		return fmt.Errorf("failed to request keyframe from webrtc source: %w", err)
	}

	maxTrys := 3

	select {
	case <-timeout.C:
		maxTrys--
		if maxTrys > 0 {
			gp.Log.Debugw("retrying keyframe request", "ssrc", wt.SSRC, "remaining", maxTrys)
			if err := gp.RequestKeyframe(wt); err != nil {
				pad.RemoveProbe(probe)
				return fmt.Errorf("failed to request keyframe from webrtc source: %w", err)
			}
			select {
			case <-timeout.C:
			case <-done:
			}
		} else {
			pad.RemoveProbe(probe)
			gp.Log.Errorw("Timeout waiting for keyframe on WebRTC source", nil, "ssrc", wt.SSRC)
			return fmt.Errorf("timeout waiting for keyframe on webrtc source ssrc: %d", wt.SSRC)
		}
	case <-done:
		if err != nil {
			return err
		}
		gp.Log.Debugw("switched selector source", "ssrc", wt.SSRC)
	}
	return nil
}

func (gp *CameraPipeline) RemoveWebRTCSourceFromSelector(ssrc uint32) error {
	gp.Mu.Lock()
	defer gp.Mu.Unlock()

	if gp.Closed() {
		return nil
	}

	webrtcTrack, exists := gp.WebrtcToSip.WebrtcTracks[ssrc]
	if !exists {
		return fmt.Errorf("webrtc source with ssrc %d does not exist", ssrc)
	}

	if err := webrtcTrack.Close(gp.Pipeline); err != nil {
		return fmt.Errorf("failed to close webrtc to selector chain: %w", err)
	}

	delete(gp.WebrtcToSip.WebrtcTracks, ssrc)

	return nil
}

func (gp *CameraPipeline) setupAutoSwitching() error {
	mu := sync.Mutex{}
	sel := gp.WebrtcToSip.InputSelector

	quickSwitch := func() error {
		activePad, err := sel.GetProperty("active-pad")
		if err != nil {
			return fmt.Errorf("failed to get active pad from selector: %w", err)
		}
		pad, ok := activePad.(*gst.Pad)
		if ok && pad != nil && pad.GetParentElement() != nil {
			return nil
		}

		mu.Lock()
		defer mu.Unlock()
		if err := gp.ensureActiveSource(); err != nil {
			gp.Log.Errorw("Failed to ensure active source during quick switch", err)
			return err
		}
		return nil
	}

	if _, err := gp.WebrtcToSip.InputSelector.Connect("pad-added", func(selector *gst.Element, pad *gst.Pad) {
		gp.Log.Debugw("selector pad-added", "pad", pad.GetName())
		if err := quickSwitch(); err != nil {
			gp.Log.Errorw("failed to ensure active source after pad-added", err)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect pad-added signal to selector: %w", err)
	}

	if _, err := gp.WebrtcToSip.InputSelector.Connect("pad-removed", func(selector *gst.Element, pad *gst.Pad) {
		gp.Log.Debugw("selector pad-removed", "pad", pad.GetName())
		if err := quickSwitch(); err != nil {
			gp.Log.Errorw("failed to ensure active source after pad-removed", err)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect pad-removed signal to selector: %w", err)
	}

	if _, err := gp.WebrtcToSip.InputSelector.Connect("notify::active-pad", func(selector *gst.Element) {
		gp.Log.Debugw("selector active-pad changed")
		if err := quickSwitch(); err != nil {
			gp.Log.Errorw("failed to ensure active source after active-pad changed", err)
		}
	}); err != nil {
		return fmt.Errorf("failed to connect notify::active-pad signal to selector: %w", err)
	}

	return nil
}

func (gp *CameraPipeline) ensureActiveSource() error {
	if gp.Closed() {
		return nil
	}

	sel := gp.WebrtcToSip.InputSelector
	activePad, err := sel.GetProperty("active-pad")
	if err == nil && activePad != nil {
		pad, ok := activePad.(*gst.Pad)
		if ok && pipeline.ValidatePad(pad) == nil {
			return nil
		}
	}

	pads, err := gp.WebrtcToSip.InputSelector.GetSinkPads()
	if err != nil {
		return fmt.Errorf("failed to get selector sink pads: %w", err)
	}
	if len(pads) == 0 {
		gp.Log.Debugw("no webrtc sources in selector")
		return nil
	}
	padsName := make([]string, 0, len(pads))
	for _, pad := range pads {
		padsName = append(padsName, pad.GetName())
	}

	for _, webrtcTrack := range gp.WebrtcToSip.WebrtcTracks {
		selPad := webrtcTrack.RtpSelPad
		if err := pipeline.ValidatePad(selPad); err != nil {
			gp.Log.Debugw("WebRTC track pad invalid", "ssrc", webrtcTrack.SSRC, "error", err)
			continue
		}
		padName := selPad.GetName()
		found := false
		for _, pName := range padsName {
			if pName == padName {
				found = true
				break
			}
		}
		if !found {
			gp.Log.Debugw("WebRTC track pad not in selector", "ssrc", webrtcTrack.SSRC, "pad", padName)
			continue
		}
		if err := gp.switchSelectorPad(webrtcTrack, selPad); err != nil {
			gp.Log.Errorw("Failed to switch selector pad to ssrc", err, "ssrc", webrtcTrack.SSRC)
			continue
		}
		gp.Log.Debugw("Switched selector pad to ssrc", "ssrc", webrtcTrack.SSRC)
		return nil
	}

	gp.Log.Debugw("no available webrtc sources for selector")
	return nil
}

func (gp *CameraPipeline) RequestKeyframe(wt *WebrtcTrack) error {
	if wt.RtpBinPad == nil {
		return fmt.Errorf("RtpBinPad is nil, cannot request keyframe (pad not yet linked)")
	}

	structure := gst.NewStructure("GstForceKeyUnit")
	structure.SetValue("timestamp", uint64(gst.ClockTimeNone))
	structure.SetValue("stream-time", uint64(gst.ClockTimeNone))
	structure.SetValue("running-time", uint64(gst.ClockTimeNone))
	structure.SetValue("all-headers", true)
	structure.SetValue("count", uint32(1))
	structure.SetValue("ssrc", uint32(wt.SSRC))

	event := gst.NewCustomEvent(gst.EventTypeCustomUpstream, structure)

	if !wt.RtpBinPad.SendEvent(event) {
		return fmt.Errorf("failed to send force key unit event to webrtc source")
	}

	return nil
}
