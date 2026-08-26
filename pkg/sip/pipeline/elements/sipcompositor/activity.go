package sipcompositor

import (
	"fmt"
	"sync"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/video"
)

const selectorSilence = 2 * time.Second

// selectorActivity tracks per-branch buffer activity on an input-selector.
type selectorActivity struct {
	mu         sync.Mutex
	lastBuffer map[string]time.Time
	activeName string
}

func (a *selectorActivity) setActive(name string) {
	a.mu.Lock()
	a.activeName = name
	a.mu.Unlock()
}

func (a *selectorActivity) forget(name string) {
	a.mu.Lock()
	delete(a.lastBuffer, name)
	if a.activeName == name {
		a.activeName = ""
	}
	a.mu.Unlock()
}

// watchSelectorSink installs a buffer probe on a selector sink pad. When a
// buffer arrives on a non-active branch while the active branch has been
// silent for more than selectorSilence, the selector switches to that branch
// and a keyframe is requested from it.
func watchSelectorSink(self *gst.Bin, e *SipCompositor, sink *gst.Pad, label string, branch func(*SipCompositor) (*gst.Element, *selectorActivity)) {
	wself := glib.WeakRefInit(self)
	eweak := weak.Make(e)

	if _, act := branch(e); act != nil {
		act.mu.Lock()
		if act.lastBuffer == nil {
			act.lastBuffer = make(map[string]time.Time)
		}
		if act.activeName == "" {
			act.activeName = sink.GetName()
		}
		act.mu.Unlock()
	}

	sink.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		e := eweak.Value()
		if e == nil {
			return gst.PadProbeOK
		}
		selector, act := branch(e)
		if selector == nil || act == nil {
			return gst.PadProbeOK
		}

		now := time.Now()
		name := pad.GetName()
		act.mu.Lock()
		act.lastBuffer[name] = now
		if act.activeName == "" {
			act.activeName = name
		}
		switched := false
		if name != act.activeName {
			if last, ok := act.lastBuffer[act.activeName]; !ok || now.Sub(last) > selectorSilence {
				act.activeName = name
				switched = true
			}
		}
		act.mu.Unlock()
		if !switched {
			return gst.PadProbeOK
		}

		if err := selector.SetProperty("active-pad", pad); err != nil {
			if self := gst.ToGstBin(wself.Get()); self != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set active-pad on %s input-selector\nerr=%v", label, err))
			}
			return gst.PadProbeOK
		}
		if self := gst.ToGstBin(wself.Get()); self != nil {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Switched %s input-selector to active branch\npad=%s", label, name))
		}
		pad.SendEvent(video.NewEventUpstreamForceKeyUnit(gst.ClockTimeNone, true, 0))
		return gst.PadProbeOK
	})
}
