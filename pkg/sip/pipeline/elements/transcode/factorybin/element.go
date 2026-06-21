package factorybin

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var CAT = gst.NewDebugCategory(
	"factorybin",
	gst.DebugColorNone,
	"factorybin Element",
)

var properties = []*glib.ParamSpec{
	glib.NewBoxedParam(
		"factories",
		"Factories",
		"The list of factories to use for transcoding",
		glib.TYPE_STRV,
		glib.ParameterWritable|glib.ParameterReadable|glib.ParameterConstructOnly,
	),
	glib.NewBoxedParam(
		"child-properties",
		"Child Properties",
		"Properties to set on the created child element, in the format factory.property=value or *.property=value to apply to all factories",
		gst.TypeStructure,
		glib.ParameterWritable|glib.ParameterConstructOnly,
	),
	glib.NewStringParam(
		"selected-factory",
		"Selected Factory",
		"The name of the factory that was selected based on the negotiated caps",
		nil,
		glib.ParameterReadable,
	),
}

type FactoryCaps struct {
	Factory  *gst.ElementFactory
	SrcCaps  *gst.Caps
	SinkCaps *gst.Caps
}

type FactoryBin struct {
	mu sync.Mutex

	Factories   []*gst.ElementFactory
	FactoryCaps []FactoryCaps

	SrcPad  *gst.GhostPad
	SinkPad *gst.GhostPad

	Elem       *gst.Element
	Properties map[string]map[string]interface{}
}

func (e *FactoryBin) New() glib.GoObjectSubclass {
	return &FactoryBin{}
}

func (e *FactoryBin) ClassInit(klass *glib.ObjectClass) {
	class := gst.ToElementClass(klass)
	class.SetMetadata(
		"Factory Bin",
		"Factory",
		"Creates elements from factories",
		"Roomkit <roomkit-visio@numerique.gouv.fr>",
	)

	class.AddPadTemplate(gst.NewPadTemplate(
		"sink",
		gst.PadDirectionSink,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.AddPadTemplate(gst.NewPadTemplate(
		"src",
		gst.PadDirectionSource,
		gst.PadPresenceAlways,
		gst.NewAnyCaps(),
	))

	class.InstallProperties(properties)
}

func (e *FactoryBin) computeCaps(instance *gst.Object, pad *gst.Pad, direction gst.PadDirection, filter *gst.Caps) *gst.Caps {
	e.mu.Lock()
	defer e.mu.Unlock()

	var otherPad *gst.Pad
	if direction == gst.PadDirectionSink {
		otherPad = e.SrcPad.Pad
	} else {
		otherPad = e.SinkPad.Pad
	}

	otherCaps := otherPad.PeerQueryCaps(nil)
	if otherCaps == nil {
		otherCaps = gst.NewAnyCaps()
	}

	result := gst.NewEmptyCaps()
	for _, fc := range e.FactoryCaps {
		var tmplCaps, othertmplCaps *gst.Caps
		if direction == gst.PadDirectionSink {
			tmplCaps = fc.SinkCaps
			othertmplCaps = fc.SrcCaps
		} else {
			tmplCaps = fc.SrcCaps
			othertmplCaps = fc.SinkCaps
		}

		if !othertmplCaps.CanIntersect(otherCaps) {
			continue
		}
		result = result.Merge(tmplCaps.Copy())
	}

	if result != nil && filter != nil {
		result = result.IntersectFull(filter, gst.CapsIntersectFirst)
	}

	return result
}

func (e *FactoryBin) selectFactory(incomingCaps *gst.Caps) *FactoryCaps {
	downstreamCaps := e.SrcPad.Pad.PeerQueryCaps(nil)
	if downstreamCaps == nil {
		downstreamCaps = gst.NewAnyCaps()
	}
	for i := range e.FactoryCaps {
		fc := &e.FactoryCaps[i]
		if !fc.SinkCaps.CanIntersect(incomingCaps) {
			continue
		}
		if !fc.SrcCaps.CanIntersect(downstreamCaps) {
			continue
		}
		return fc
	}
	return nil
}

func (e *FactoryBin) currentFactoryLinksDownstream() bool {
	if e.Elem == nil {
		return true
	}
	downstreamCaps := e.SrcPad.Pad.PeerQueryCaps(nil)
	if downstreamCaps == nil {
		return true
	}
	caps := e.Elem.GetStaticPad("src").QueryCaps(nil)
	return caps != nil && caps.CanIntersect(downstreamCaps)
}

func (e *FactoryBin) createChild(self *gst.Bin, fc *FactoryCaps) *gst.Element {
	properties := make(map[string]interface{})
	maps.Copy(properties, e.Properties["*"])
	if factoryProperties, ok := e.Properties[fc.Factory.GetName()]; ok {
		maps.Copy(properties, factoryProperties)
	}

	elem, err := gst.NewElementWithProperties(fc.Factory.GetName(), properties)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create element from factory\nfactory=%s\nerr=%v", fc.Factory.GetName(), err))
		self.Error("Failed to create element from factory", err)
		return nil
	}

	if err := self.Add(elem); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add element to bin\nelement=%s\nerr=%v", elem.GetName(), err))
		self.Error("Failed to add element to bin", err)
		return nil
	}

	return elem
}

func (e *FactoryBin) reconfigure(self *gst.Bin, caps *gst.Caps) bool {
	e.mu.Lock()

	if e.Elem != nil {
		if e.Elem.GetStaticPad("sink").QueryAcceptCaps(caps) && e.currentFactoryLinksDownstream() {
			e.mu.Unlock()
			return true
		}
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Current element no longer fits caps, trying to select a new factory\nelement=%s\ncaps=%q", e.Elem.GetName(), caps.String()))
	}

	fc := e.selectFactory(caps)
	if fc == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No factory found for caps\ncaps=%q", caps.String()))
		e.mu.Unlock()
		return false
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Selected factory for caps\nfactory=%s\ncaps=%q", fc.Factory.GetName(), caps.String()))

	elem := e.createChild(self, fc)
	if elem == nil {
		e.mu.Unlock()
		return false
	}

	oldElem := e.Elem
	e.Elem = elem
	sinkPad, srcPad := e.SinkPad, e.SrcPad
	e.mu.Unlock()

	var parkProbeID uint64
	if oldElem != nil {
		parkProbeID = sinkPad.Pad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			return gst.PadProbeOK
		})

		oldElem.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList|gst.PadProbeTypeEventDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			return gst.PadProbeDrop
		})

		sinkPad.GetInternal().Pad.AddProbe(gst.PadProbeTypeIdle, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			if err := oldElem.SetState(gst.StateNull); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set state of old element to NULL\nelement=%s\nerr=%v", oldElem.GetName(), err))
			}
			if err := self.Remove(oldElem); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove old element from bin\nelement=%s\nerr=%v", oldElem.GetName(), err))
			}
			return gst.PadProbeRemove
		})
	}

	srcPad.SetTarget(elem.GetStaticPad("src"))
	sinkPad.SetTarget(elem.GetStaticPad("sink"))

	if !elem.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for element\nelement=%s", elem.GetName()))
	}

	if parkProbeID != 0 {
		sinkPad.Pad.RemoveProbe(parkProbeID)
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Factory successfully configured for caps\nfactory=%s\ncaps=%q", fc.Factory.GetName(), caps.String()))

	return true
}

func (e *FactoryBin) onCapsEvent(instance *gst.Object, pad *gst.Pad, event *gst.Event) bool {
	self := gst.ToGstBin(instance)
	if !e.reconfigure(self, event.ParseCaps()) {
		return false
	}
	return pad.EventDefault(instance, event)
}

func (e *FactoryBin) onReconfigureEvent(instance *gst.Object, pad *gst.Pad, event *gst.Event) bool {
	self := gst.ToGstBin(instance)
	if caps := e.SinkPad.Pad.CurrentCaps(); caps != nil {
		e.reconfigure(self, caps)
	}
	return pad.EventDefault(instance, event)
}

func (e *FactoryBin) InstanceInit(instance *glib.Object) {
	e.mu.Lock()
	defer e.mu.Unlock()

	self := gst.ToGstBin(instance)
	elemClass := gst.ToElementClass(self.Class())

	e.Properties = make(map[string]map[string]interface{})
	e.Properties["*"] = make(map[string]interface{})

	eweak := weak.Make(e)

	e.SinkPad = gst.NewGhostPadNoTargetFromTemplate("sink", elemClass.GetPadTemplate("sink"))
	e.SinkPad.SetQueryFunction(func(pad *gst.Pad, instance *gst.Object, query *gst.Query) bool {
		switch query.Type() {
		case gst.QueryCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			filter := query.ParseCaps()
			resultCaps := e.computeCaps(instance, pad, gst.PadDirectionSink, filter)
			if resultCaps == nil {
				return false
			}
			if e.Elem != nil {
				resultCaps = e.Elem.GetStaticPad("sink").QueryCaps(filter).Merge(resultCaps)
			}
			query.SetCapsResult(resultCaps)
			return true
		case gst.QueryAcceptCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			acceptCaps := query.ParseAcceptCaps()
			if e.Elem != nil && e.Elem.GetStaticPad("sink").QueryAcceptCaps(acceptCaps) {
				query.SetAcceptCapsResult(true)
				return true
			}
			resultCaps := e.computeCaps(instance, pad, gst.PadDirectionSink, acceptCaps)
			accepted := resultCaps != nil && !resultCaps.IsEmpty()
			query.SetAcceptCapsResult(accepted)
			return true
		default:
			return pad.QueryDefault(instance, query)
		}
	})
	e.SinkPad.SetEventFunction(func(pad *gst.Pad, instance *gst.Object, event *gst.Event) bool {
		switch event.Type() {
		case gst.EventTypeCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			return e.onCapsEvent(instance, pad, event)
		default:
			return pad.EventDefault(instance, event)
		}
	})
	self.AddPad(e.SinkPad.Pad)

	e.SrcPad = gst.NewGhostPadNoTargetFromTemplate("src", elemClass.GetPadTemplate("src"))
	e.SrcPad.SetQueryFunction(func(pad *gst.Pad, instance *gst.Object, query *gst.Query) bool {
		switch query.Type() {
		case gst.QueryCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			filter := query.ParseCaps()
			resultCaps := e.computeCaps(instance, pad, gst.PadDirectionSource, filter)
			if resultCaps == nil {
				return false
			}
			if e.Elem != nil {
				resultCaps = e.Elem.GetStaticPad("src").QueryCaps(filter).Merge(resultCaps)
			}
			query.SetCapsResult(resultCaps)
			return true
		case gst.QueryAcceptCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			acceptCaps := query.ParseAcceptCaps()
			if e.Elem != nil && e.Elem.GetStaticPad("src").QueryAcceptCaps(acceptCaps) {
				query.SetAcceptCapsResult(true)
				return true
			}
			resultCaps := e.computeCaps(instance, pad, gst.PadDirectionSource, acceptCaps)
			accepted := resultCaps != nil && !resultCaps.IsEmpty()
			query.SetAcceptCapsResult(accepted)
			return true
		default:
			return pad.QueryDefault(instance, query)
		}
	})
	e.SrcPad.SetEventFunction(func(pad *gst.Pad, instance *gst.Object, event *gst.Event) bool {
		switch event.Type() {
		case gst.EventTypeReconfigure:
			e := eweak.Value()
			if e == nil {
				return false
			}
			return e.onReconfigureEvent(instance, pad, event)
		default:
			return pad.EventDefault(instance, event)
		}
	})
	self.AddPad(e.SrcPad.Pad)
}

func (e *FactoryBin) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	e.mu.Lock()
	defer e.mu.Unlock()

	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "factories":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting factories property value\nerr=%v", err))
			return
		}
		val, ok := gv.(*glib.Strv)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for factories property")
			return
		}
		if val == nil {
			return
		}

		factories := make([]*gst.ElementFactory, 0, val.Len())
		for _, factoryName := range val.Strings() {
			factory := gst.Find(factoryName)
			if factory == nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Factory not found\nfactory=%s", factoryName))
				continue
			}
			factories = append(factories, factory)
		}
		e.Factories = factories
	case "child-properties":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting child-properties property value\nerr=%v", err))
			return
		}
		val, ok := gv.(*gst.Structure)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for child-properties property")
			return
		}
		if val == nil {
			return
		}

		properties := val.Values()
		for k, v := range properties {
			parts := strings.SplitN(k, ".", 2)
			if len(parts) != 2 {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid child property key\nkey=%s", k))
				continue
			}
			factoryName := parts[0]
			propertyName := parts[1]

			if _, ok := e.Properties[factoryName]; !ok {
				e.Properties[factoryName] = make(map[string]interface{})
			}
			e.Properties[factoryName][propertyName] = v
		}
	}
}

func (e *FactoryBin) GetProperty(instance *glib.Object, id uint) *glib.Value {
	e.mu.Lock()
	defer e.mu.Unlock()

	self := gst.ToGstBin(instance)
	param := properties[id]
	switch param.Name() {
	case "factories":
		names := make([]string, 0, len(e.Factories))
		for _, factory := range e.Factories {
			names = append(names, factory.GetName())
		}
		strv := glib.NewStrv(names)
		value, err := glib.GValue(strv)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GValue for factories property\nerr=%v", err))
			self.Error("Error creating GValue for factories property", err)
			return nil
		}
		return value
	case "selected-factory":
		if e.Elem == nil {
			return nil
		}
		factoryName := e.Elem.GetFactory().GetName()
		value, err := glib.GValue(factoryName)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GValue for selected-factory property\nerr=%v", err))
			self.Error("Error creating GValue for selected-factory property", err)
			return nil
		}
		return value
	}
	return nil
}

func (e *FactoryBin) Constructed(instance *glib.Object) {
	e.mu.Lock()
	defer e.mu.Unlock()

	self := gst.ToGstBin(instance)

	e.FactoryCaps = make([]FactoryCaps, 0, len(e.Factories))
	for _, factory := range e.Factories {
		var srcCaps, sinkCaps *gst.Caps
		templates := factory.GetStaticPadTemplates()
		for _, t := range templates {
			if t.Presence() != gst.PadPresenceAlways {
				continue
			}
			if t.Name() == "src" && t.Direction() == gst.PadDirectionSource {
				srcCaps = t.Caps()
			} else if t.Name() == "sink" && t.Direction() == gst.PadDirectionSink {
				sinkCaps = t.Caps()
			}
			switch t.Direction() {
			case gst.PadDirectionSource:
				srcCaps = t.Caps()
			case gst.PadDirectionSink:
				sinkCaps = t.Caps()
			}
		}
		e.FactoryCaps = append(e.FactoryCaps, FactoryCaps{
			Factory:  factory,
			SrcCaps:  srcCaps,
			SinkCaps: sinkCaps,
		})
	}

	e.FactoryCaps = slices.DeleteFunc(e.FactoryCaps, func(c FactoryCaps) bool {
		if c.SrcCaps == nil || c.SinkCaps == nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Factory does not have both src and sink pads with always presence, skipping\nfactory=%s", c.Factory.GetName()))
			return true
		}
		return false
	})

	var msg strings.Builder
	msg.WriteString("Available factories and their caps:\n")
	for _, fc := range e.FactoryCaps {
		msg.WriteString(fmt.Sprintf("- %s: src caps: %q, sink caps: %q\n", fc.Factory.GetName(), fc.SrcCaps.String(), fc.SinkCaps.String()))
	}
	self.Log(CAT, gst.LevelInfo, msg.String())
}

func (e *FactoryBin) Finalize(instance *glib.Object) {
	self := gst.ToGstBin(instance)
	self.Log(CAT, gst.LevelDebug, "Finalizing FactoryBin element")

	e.mu.Lock()
	defer e.mu.Unlock()

	e.Factories = nil
	e.FactoryCaps = nil
	e.Elem = nil
	e.SrcPad = nil
	e.SinkPad = nil
	e.Properties = nil
}
