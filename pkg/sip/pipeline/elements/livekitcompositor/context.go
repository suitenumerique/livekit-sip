package livekitcompositor

import (
	"encoding/json"
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

const ContextOverlayScreen = "livekit.compositor.overlay.screen"

// NewContextOverlayScreen shows s in place of the mosaic; nil hides the screen.
func NewContextOverlayScreen(s *Screen) *gst.Context {
	ctx := gst.NewContext(ContextOverlayScreen, false)
	st := ctx.WritableStructure()
	if s == nil {
		st.SetBool("show", false)
		return ctx
	}
	data, _ := json.Marshal(s)
	st.SetBool("show", true)
	st.SetString("screen", string(data))
	return ctx
}

func GetContextOverlayScreen(ctx *gst.Context) *Screen {
	if ctx == nil || !ctx.HasContextType(ContextOverlayScreen) {
		return nil
	}
	st := ctx.GetStructure()
	show, err := st.GetBool("show")
	if err != nil || !show {
		return nil
	}
	data, err := st.GetString("screen")
	if err != nil {
		return nil
	}
	var s Screen
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return nil
	}
	return &s
}

func (e *LivekitCompositor) SetContext(instance *gst.Element, ctx *gst.Context) {
	self := gst.ToGstBin(instance)

	switch ctx.GetType() {
	case ContextOverlayScreen:
		s := GetContextOverlayScreen(ctx)
		title := ""
		if s != nil {
			title = s.Title
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Setting overlay screen context\nshow=%t\ntitle=%q", s != nil, title))
		e.mu.Lock()
		e.overlayScreen = s
		e.refreshOverlayCache()
		e.mu.Unlock()
	}
}
