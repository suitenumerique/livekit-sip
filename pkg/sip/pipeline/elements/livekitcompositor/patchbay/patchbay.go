package patchbay

import (
	"fmt"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/samber/lo"
)

var CAT *gst.DebugCategory

type Patchbay struct {
	mu sync.Mutex

	Sinks   []*Sink
	Sources []*Source
}

type Sink struct {
	OutputSelector *gst.Element
	Links          map[string]string
}

type Source struct {
	Links         map[string]string
	InputSelector *gst.Element
}

func (e *Patchbay) New() glib.GoObjectSubclass {
	return &Patchbay{}
}

// ClassInit implements [glib.GoObjectSubclass].
func (e *Patchbay) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"LiveKit Compositor",
		"Transform",
		"Element to composite multiple LiveKit tracks into a single video stream",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	// action signals
	gst.SignalNew(
		class.Type(),
		"activate-path",
		gst.SignalRunLast,
		glib.TYPE_NONE,
		gst.TypePad,
		gst.TypePad,
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink_%u",
		gst.PadDirectionSink,
		gst.PadPresenceRequest,
		gst.NewAnyCaps(),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src_%u",
		gst.PadDirectionSource,
		gst.PadPresenceRequest,
		gst.NewAnyCaps(),
	))

	// class.InstallProperties(properties)
}

func (e *Patchbay) InstanceInit(instance *glib.Object) {
	self := gst.ToGstBin(instance)

	eweak := weak.Make(e)
	if _, err := self.Connect("activate-path", func(instance *gst.Element, sinkPad *gst.Pad, sourcePad *gst.Pad) {
		ptr := eweak.Value()
		if ptr == nil {
			return
		}
		ptr.onActivatePath(instance, sinkPad, sourcePad)
	}); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to connect activate-path signal: %v", err))
		self.Error("Failed to connect activate-path signal", err)
	}
}

func (e *Patchbay) ChangeState(instance *gst.Element, transition gst.StateChange) gst.StateChangeReturn {
	self := gst.ToGstBin(instance)

	if transition == gst.StateChangeReadyToNull {
		e.releaseAllPads(self)
	}

	ret := self.ParentChangeState(transition)

	if transition == gst.StateChangeReadyToNull {
		e.mu.Lock()
		defer e.mu.Unlock()

		for _, sink := range e.Sinks {
			if sink == nil {
				continue
			}
			sink.OutputSelector = nil
			sink.Links = nil
		}

		for _, source := range e.Sources {
			if source == nil {
				continue
			}
			source.InputSelector = nil
			source.Links = nil
		}

		e.Sinks = nil
		e.Sources = nil
	}

	return ret
}

func (e *Patchbay) RequestNewPad(instance *gst.Element, templ *gst.PadTemplate, name string, caps *gst.Caps) *gst.Pad {
	self := gst.ToGstBin(instance)

	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown pad template for requested pad: %s", name))
		return nil
	}

	switch templ.Name() {
	case "sink_%u":
		return e.requestNewSinkPad(self, templ, name, caps)
	case "src_%u":
		return e.requestNewSourcePad(self, templ, name, caps)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No handler for requested pad template: %s", templ.Name()))
		return nil
	}
}

func (e *Patchbay) requestNewSinkPad(self *gst.Bin, templ *gst.PadTemplate, _ string, _ *gst.Caps) *gst.Pad {
	e.mu.Lock()
	defer e.mu.Unlock()

	if lo.CountBy(e.Sources, func(s *Source) bool { return s != nil }) == 0 {
		self.Log(CAT, gst.LevelError, "Cannot create sink pad without any source pads available")
		return nil
	}

	sink := &Sink{
		Links: make(map[string]string),
	}
	idx := lo.IndexOf(e.Sinks, nil)
	if idx == -1 {
		idx = len(e.Sinks)
		e.Sinks = append(e.Sinks, sink)
	} else {
		e.Sinks[idx] = sink
	}
	pname := fmt.Sprintf("sink_%d", idx)

	if self.GetStaticPad(pname) != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Sink pad with name %s already exists", pname))
		e.Sinks[idx] = nil
		return nil
	}

	var err error
	sink.OutputSelector, err = gst.NewElementWithProperties("output-selector", map[string]interface{}{
		"name":                 pname,
		"pad-negotiation-mode": int(2), // active
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create output selector: %v", err))
		self.Error("Failed to create output selector", err)
		e.Sinks[idx] = nil
		return nil
	}

	if err := self.Add(sink.OutputSelector); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add output selector to bin: %v", err))
		self.Error("Failed to add output selector to bin", err)
		e.Sinks[idx] = nil
		return nil
	}

	for _, source := range e.Sources {
		if source == nil {
			continue
		}
		if err := e.linkSinkSource(sink, source); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new sink to existing source: %v", err))
			self.Error("Failed to link new sink to existing source", err)

			// for _, source := range e.Sources[:i] { // i or i-1?
			// 	if source != nil {
			// 		if err := e.unlinkSinkSource(sink, source); err != nil {
			// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unlink sink from source during cleanup: %v", err))
			// 		}
			// 	}
			// }

			// e.Sinks[idx] = nil
			return nil
		}
	}

	gpad := gst.NewGhostPadFromTemplate(pname, sink.OutputSelector.GetStaticPad("sink"), templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for new sink")
		return nil
	}

	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for new sink")
		return nil
	}

	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to bin for new sink: %s", gpad.GetName()))
		return nil
	}

	if !sink.OutputSelector.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of output selector (%s) with parent", sink.OutputSelector.GetName()))
	}

	return gpad.Pad
}

func (e *Patchbay) requestNewSourcePad(self *gst.Bin, templ *gst.PadTemplate, _ string, _ *gst.Caps) *gst.Pad {
	e.mu.Lock()
	defer e.mu.Unlock()

	source := &Source{
		Links: make(map[string]string),
	}
	idx := lo.IndexOf(e.Sources, nil)
	if idx == -1 {
		idx = len(e.Sources)
		e.Sources = append(e.Sources, source)
	} else {
		e.Sources[idx] = source
	}
	pname := fmt.Sprintf("src_%d", idx)

	if self.GetStaticPad(pname) != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Source pad with name %s already exists", pname))
		e.Sources[idx] = nil
		return nil
	}

	var err error
	source.InputSelector, err = gst.NewElementWithProperties("input-selector", map[string]interface{}{
		"name":         pname,
		"sync-streams": false,
	})
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create input selector: %v", err))
		self.Error("Failed to create input selector", err)
		e.Sources[idx] = nil
		return nil
	}

	if err := self.Add(source.InputSelector); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add input selector to bin: %v", err))
		self.Error("Failed to add input selector to bin", err)
		e.Sources[idx] = nil
		return nil
	}

	for _, sink := range e.Sinks {
		if sink == nil {
			continue
		}
		if err := e.linkSinkSource(sink, source); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to link new source to existing sink: %v", err))
			self.Error("Failed to link new source to existing sink", err)

			// for _, sink := range e.Sinks[:i] { // i or i-1?
			// 	if sink != nil {
			// 		if err := e.unlinkSinkSource(sink, source); err != nil {
			// 			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unlink sink from source during cleanup: %v", err))
			// 		}
			// 	}
			// }

			// e.Sources[idx] = nil
			return nil
		}
	}

	gpad := gst.NewGhostPadFromTemplate(pname, source.InputSelector.GetStaticPad("src"), templ)
	if gpad == nil {
		self.Log(CAT, gst.LevelError, "Failed to create ghost pad for new source")
		return nil
	}

	if !gpad.SetActive(true) {
		self.Log(CAT, gst.LevelError, "Failed to activate ghost pad for new source")
		return nil
	}

	if !self.AddPad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add ghost pad to bin for new source: %s", gpad.GetName()))
		return nil
	}

	if !source.InputSelector.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state of input selector (%s) with parent", source.InputSelector.GetName()))
	}

	return gpad.Pad
}

func (e *Patchbay) ReleasePad(instance *gst.Element, pad *gst.Pad) {
	self := gst.ToGstBin(instance)

	if pad == nil {
		self.Log(CAT, gst.LevelWarning, "Attempted to release a nil pad")
		return
	}

	gpad := pad.AsGhostPad()
	if gpad == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Attempted to release a non-ghost pad: %s", pad.GetName()))
		return
	}

	templ := gpad.Template()
	if templ == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Attempted to release a pad with a nil template: %s", pad.GetName()))
		return
	}

	switch templ.Name() {
	case "sink_%u":
		e.releaseSinkPad(self, gpad)
	case "src_%u":
		e.releaseSourcePad(self, gpad)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No handler for released pad template: %s", templ.Name()))
	}
}

func (e *Patchbay) releaseSinkPad(self *gst.Bin, gpad *gst.GhostPad) {
	e.mu.Lock()
	defer e.mu.Unlock()

	sink, idx, ok := lo.FindIndexOf(e.Sinks, func(s *Sink) bool { return s != nil && s.OutputSelector.GetName() == gpad.GetName() })
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Attempted to release a non-existent sink pad: %s", gpad.GetName()))
		return
	}

	e.Sinks[idx] = nil

	for _, source := range e.Sources {
		if source == nil {
			continue
		}
		if err := e.unlinkSinkSource(sink, source); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unlink sink from source during release: %v", err))
		}
	}

	if err := sink.OutputSelector.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set output selector to NULL during release: %v", err))
	}

	if err := self.Remove(sink.OutputSelector); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove output selector from bin during release: %v", err))
	}

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad from bin during release: %s", gpad.GetName()))
	}
}

func (e *Patchbay) releaseSourcePad(self *gst.Bin, gpad *gst.GhostPad) {
	e.mu.Lock()
	defer e.mu.Unlock()

	source, idx, ok := lo.FindIndexOf(e.Sources, func(s *Source) bool { return s != nil && s.InputSelector.GetName() == gpad.GetName() })
	if !ok {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Attempted to release a non-existent source pad: %s", gpad.GetName()))
		return
	}

	if lo.CountBy(e.Sinks, func(s *Sink) bool { return s != nil }) != 0 && lo.CountBy(e.Sources, func(s *Source) bool { return s != nil }) <= 1 {
		self.Log(CAT, gst.LevelError, "Cannot release source pad while sink pads are still present")
		return
	}

	e.Sources[idx] = nil

	for _, sink := range e.Sinks {
		if sink == nil {
			continue
		}
		if err := e.unlinkSinkSource(sink, source); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to unlink sink from source during release: %v", err))
		}
	}

	if err := source.InputSelector.SetState(gst.StateNull); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to set input selector to NULL during release: %v", err))
	}

	if err := self.Remove(source.InputSelector); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove input selector from bin during release: %v", err))
	}

	if !self.RemovePad(gpad.Pad) {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to remove ghost pad from bin during release: %s", gpad.GetName()))
	}
}

func (e *Patchbay) releaseAllPads(self *gst.Bin) {
	sinks, err := self.GetSinkPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get sink pads during releaseAllPads: %v", err))
	} else {
		for _, pad := range sinks {
			gpad := pad.AsGhostPad()
			if gpad != nil {
				e.releaseSinkPad(self, gpad)
			}
		}
	}

	sources, err := self.GetSrcPads()
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get source pads during releaseAllPads: %v", err))
	} else {
		for _, pad := range sources {
			gpad := pad.AsGhostPad()
			if gpad != nil {
				e.releaseSourcePad(self, gpad)
			}
		}
	}
}
