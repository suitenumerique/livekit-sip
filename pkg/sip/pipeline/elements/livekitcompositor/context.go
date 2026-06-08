package livekitcompositor

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

const ContextOverlayMessage = "livekit.compositor.overlay.message"

type overlayMessage struct {
	Message string
	Level   gst.DebugLevel
	Show    bool
}

func NewContextOverlayMessage(message string, level gst.DebugLevel, show bool) *gst.Context {
	ctx := gst.NewContext(ContextOverlayMessage, false)
	s := ctx.WritableStructure()
	s.SetString("message", message)
	s.SetInt("level", int(level))
	s.SetBool("show", show)
	return ctx
}

func GetContextOverlayMessage(ctx *gst.Context) (message string, level gst.DebugLevel, show bool) {
	if ctx == nil || !ctx.HasContextType(ContextOverlayMessage) {
		return "", 0, false
	}
	s := ctx.GetStructure()
	msg, err := s.GetString("message")
	if err != nil {
		return "", 0, false
	}
	l, err := s.GetInt("level")
	if err != nil {
		return "", 0, false
	}
	ok, err := s.GetBool("show")
	if err != nil {
		return "", 0, false
	}
	return msg, gst.DebugLevel(l), ok
}

func (e *LivekitCompositor) SetContext(instance *gst.Element, ctx *gst.Context) {
	self := gst.ToGstBin(instance)

	switch ctx.GetType() {
	case ContextOverlayMessage:
		message, level, show := GetContextOverlayMessage(ctx)
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Setting overlay message context: message=%s, level=%d, show=%t", message, level, show))
		e.mu.Lock()
		e.overlayMessage = overlayMessage{
			Message: message,
			Level:   level,
			Show:    show,
		}
		e.refreshOverlayCache()
		e.mu.Unlock()
	}
}
