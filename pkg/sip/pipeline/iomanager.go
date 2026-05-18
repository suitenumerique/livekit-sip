package pipeline

import (
	"fmt"
	"strings"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

func NewIOChain(log logger.Logger, parent *Pipeline) *IOManager {
	return &IOManager{
		log:      log,
		pipeline: parent,
	}
}

type IOManager struct {
	pipeline *Pipeline
	log      logger.Logger

	SipController     *gst.Element
	LivekitController *gst.Element
}

var _ GstChain = (*IOManager)(nil)

// Create implements [GstChain].
func (c *IOManager) Create() error {
	var err error

	c.SipController, err = gst.NewElementWithProperties("iosip", map[string]interface{}{
		"video-width":  c.pipeline.videoWidth,
		"video-height": c.pipeline.videoHeight,
		"framerate":    c.pipeline.videoFramerate,
	})
	if err != nil {
		return fmt.Errorf("failed to create IO Manager SIP element: %w", err)
	}

	c.LivekitController, err = gst.NewElementWithProperties("iolivekit", map[string]interface{}{
		"video-width":  c.pipeline.videoWidth,
		"video-height": c.pipeline.videoHeight,
		"framerate":    c.pipeline.videoFramerate,
	})
	if err != nil {
		return fmt.Errorf("failed to create IO Manager LiveKit element: %w", err)
	}

	return nil
}

func (c *IOManager) Add() error {
	if err := c.pipeline.Pipeline().AddMany(
		c.SipController,
		c.LivekitController,
	); err != nil {
		return fmt.Errorf("failed to add IO Manager elements to pipeline: %w", err)
	}
	return nil
}

func (c *IOManager) handleSipControllerPadAdded(_ *gst.Element, pad *gst.Pad) {
	pname := pad.GetName()
	c.log.Debugw("SIP IO Manager pad added", "pad", pname)

	if !strings.HasPrefix(pname, "send_rtp_src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session); err != nil {
		c.log.Errorw("Failed to parse pad name", err, "pad", pname)
		return
	}

	switch livekit.TrackSource(session) {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE:
	default:
		c.log.Warnw("Unknown session kind in SIP controller pad name", nil, "session", session, "pad", pname)
		return
	}

	destPad := c.pipeline.WebrtcIo.LivekitBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if destPad == nil {
		c.log.Warnw("Failed to get request pad", nil, "pad", fmt.Sprintf("send_rtp_sink_%d", session))
		return
	}

	if ret := pad.Link(destPad); ret != gst.PadLinkOK {
		c.log.Errorw("Failed to link pads", fmt.Errorf("pad link result: %v", ret), "src_pad", pad.GetName(), "dest_pad", destPad.GetName())
		return
	}
}

func (c *IOManager) handleSipControllerPadRemoved(_ *gst.Element, pad *gst.Pad) {
	pname := pad.GetName()
	c.log.Debugw("SIP IO Manager pad removed", "pad", pname)

	if !strings.HasPrefix(pname, "send_rtp_src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session); err != nil {
		c.log.Errorw("Failed to parse pad name", err, "pad", pname)
		return
	}

	if c.pipeline.WebrtcIo == nil || c.pipeline.WebrtcIo.LivekitBin == nil {
		return
	}
	destPad := c.pipeline.WebrtcIo.LivekitBin.GetStaticPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if destPad == nil {
		c.log.Warnw("Failed to get static pad", nil, "pad", fmt.Sprintf("send_rtp_sink_%d", session))
		return
	}
	c.pipeline.WebrtcIo.LivekitBin.ReleaseRequestPad(destPad)
	c.log.Infow("Released request pad on LiveKit bin", "pad", destPad.GetName())
}

func (c *IOManager) handleLivekitCompositorPadAdded(_ *gst.Element, pad *gst.Pad) {
	pname := pad.GetName()
	c.log.Debugw("Livekit IO Manager pad added", "pad", pname)

	if !strings.HasPrefix(pname, "send_rtp_src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session); err != nil {
		c.log.Errorw("Failed to parse pad name", err, "pad", pname)
		return
	}

	destPad := c.pipeline.SipIo.SipBin.GetRequestPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if destPad == nil {
		c.log.Warnw("Failed to get request pad", nil, "pad", fmt.Sprintf("send_rtp_sink_%d", session))
		return
	}

	if ret := pad.Link(destPad); ret != gst.PadLinkOK {
		c.log.Errorw("Failed to link pads", fmt.Errorf("pad link result: %v", ret), "src_pad", pad.GetName(), "dest_pad", destPad.GetName())
		return
	}
}

func (c *IOManager) handleLivekitCompositorPadRemoved(_ *gst.Element, pad *gst.Pad) {
	pname := pad.GetName()
	c.log.Debugw("Livekit IO Manager pad removed", "pad", pname)

	if !strings.HasPrefix(pname, "send_rtp_src_") {
		return
	}

	var session int
	if _, err := fmt.Sscanf(pname, "send_rtp_src_%d", &session); err != nil {
		c.log.Errorw("Failed to parse pad name", err, "pad", pname)
		return
	}

	if c.pipeline.SipIo == nil || c.pipeline.SipIo.SipBin == nil {
		return
	}
	destPad := c.pipeline.SipIo.SipBin.GetStaticPad(fmt.Sprintf("send_rtp_sink_%d", session))
	if destPad == nil {
		c.log.Warnw("Failed to get static pad", nil, "pad", fmt.Sprintf("send_rtp_sink_%d", session))
		return
	}
	c.pipeline.SipIo.SipBin.ReleaseRequestPad(destPad)
	c.log.Infow("Released request pad on SIP bin", "pad", destPad.GetName())
}

func (c *IOManager) toggleScreenshare(e *gst.Element, hasScreenshare bool) {
	if _, err := c.pipeline.SipIo.SipBin.Emit("toggle-screenshare", hasScreenshare); err != nil {
		c.log.Errorw("Failed to emit toggle-screenshare signal", err)
	}
}

func (c *IOManager) Link() error {
	cweak := weak.Make(c)

	c.SipController.Connect("pad-added", func(e *gst.Element, pad *gst.Pad) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.handleSipControllerPadAdded(e, pad)
	})

	c.SipController.Connect("pad-removed", func(e *gst.Element, pad *gst.Pad) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.handleSipControllerPadRemoved(e, pad)
	})

	c.LivekitController.Connect("pad-added", func(e *gst.Element, pad *gst.Pad) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.handleLivekitCompositorPadAdded(e, pad)
	})

	c.LivekitController.Connect("pad-removed", func(e *gst.Element, pad *gst.Pad) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.handleLivekitCompositorPadRemoved(e, pad)
	})

	if _, err := c.LivekitController.Connect("has-screenshare", func(e *gst.Element, hasScreenshare bool) {
		ptr := cweak.Value()
		if ptr == nil {
			return
		}
		ptr.toggleScreenshare(e, hasScreenshare)
	}); err != nil {
		return fmt.Errorf("failed to connect to has-screenshare signal: %w", err)
	}

	return nil
}

func (c *IOManager) Close() error {
	if err := c.pipeline.Pipeline().RemoveMany(
		c.SipController,
		c.LivekitController,
	); err != nil {
		return fmt.Errorf("failed to remove IO Manager elements from pipeline: %w", err)
	}
	c.SipController = nil
	c.LivekitController = nil
	return nil
}
