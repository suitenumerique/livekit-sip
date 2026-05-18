package patchbay

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/samber/lo"
)

func (e *Patchbay) linkSinkSource(sink *Sink, source *Source) error {
	sourcePad := source.InputSelector.GetRequestPad("sink_%u")
	if sourcePad == nil {
		return fmt.Errorf("failed to get request pad from source input selector")
	}

	sinkPad := sink.OutputSelector.GetRequestPad("src_%u")
	if sinkPad == nil {
		source.InputSelector.ReleaseRequestPad(sourcePad)
		return fmt.Errorf("failed to get request pad from sink output selector")
	}

	if ret := sinkPad.Link(sourcePad); ret != gst.PadLinkOK {
		source.InputSelector.ReleaseRequestPad(sourcePad)
		sink.OutputSelector.ReleaseRequestPad(sinkPad)
		return fmt.Errorf("failed to link source pad to sink pad")
	}

	source.Links[sink.OutputSelector.GetName()] = sourcePad.GetName()
	sink.Links[source.InputSelector.GetName()] = sinkPad.GetName()

	return nil
}

func (e *Patchbay) unlinkSinkSource(sink *Sink, source *Source) error {
	sourcePadName, ok := source.Links[sink.OutputSelector.GetName()]
	if !ok {
		return fmt.Errorf("no link found from source to sink")
	}

	sinkPadName, ok := sink.Links[source.InputSelector.GetName()]
	if !ok {
		return fmt.Errorf("no link found from sink to source")
	}

	sourcePad := source.InputSelector.GetStaticPad(sourcePadName)
	if sourcePad == nil {
		return fmt.Errorf("failed to get source pad for unlinking")
	}

	sinkPad := sink.OutputSelector.GetStaticPad(sinkPadName)
	if sinkPad == nil {
		return fmt.Errorf("failed to get sink pad for unlinking")
	}

	if !sinkPad.Unlink(sourcePad) {
		return fmt.Errorf("failed to unlink source pad from sink pad")
	}

	source.InputSelector.ReleaseRequestPad(sourcePad)
	sink.OutputSelector.ReleaseRequestPad(sinkPad)

	delete(source.Links, sink.OutputSelector.GetName())
	delete(sink.Links, source.InputSelector.GetName())

	return nil
}

func (e *Patchbay) activatePath(sink *Sink, source *Source) error {
	sourcePadName, ok := source.Links[sink.OutputSelector.GetName()]
	if !ok {
		return fmt.Errorf("no link found from source to sink for activation")
	}

	sinkPadName, ok := sink.Links[source.InputSelector.GetName()]
	if !ok {
		return fmt.Errorf("no link found from sink to source for activation")
	}

	sourcePad := source.InputSelector.GetStaticPad(sourcePadName)
	if sourcePad == nil {
		return fmt.Errorf("failed to get source pad for activation")
	}

	sinkPad := sink.OutputSelector.GetStaticPad(sinkPadName)
	if sinkPad == nil {
		return fmt.Errorf("failed to get sink pad for activation")
	}

	if err := sink.OutputSelector.SetProperty("active-pad", sinkPad); err != nil {
		return fmt.Errorf("failed to set active pad on sink output selector: %v", err)
	}

	if err := source.InputSelector.SetProperty("active-pad", sourcePad); err != nil {
		return fmt.Errorf("failed to set active pad on source input selector: %v", err)
	}

	return nil
}

func (e *Patchbay) onActivatePath(instance *gst.Element, sinkPad *gst.Pad, sourcePad *gst.Pad) {
	self := gst.ToGstBin(instance)

	e.mu.Lock()
	defer e.mu.Unlock()

	if sinkPad == nil || sourcePad == nil {
		self.Log(CAT, gst.LevelError, "activate-path signal received with nil pad(s)")
		return
	}

	sinkName := sinkPad.GetName()
	sourceName := sourcePad.GetName()

	sink, ok := lo.Find(e.Sinks, func(s *Sink) bool { return s != nil && s.OutputSelector.GetName() == sinkName })
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("activate-path signal received for unknown sink pad: %s", sinkName))
		return
	}

	source, ok := lo.Find(e.Sources, func(s *Source) bool { return s != nil && s.InputSelector.GetName() == sourceName })
	if !ok {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("activate-path signal received for unknown source pad: %s", sourceName))
		return
	}

	if err := e.activatePath(sink, source); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to activate path from source %s to sink %s: %v", sourceName, sinkName, err))
		return
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Activated path from source %s to sink %s", sourceName, sinkName))
}
