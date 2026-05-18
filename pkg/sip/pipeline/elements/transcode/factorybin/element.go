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

func (e *FactoryBin) onCapsEvent(instance *gst.Object, pad *gst.Pad, event *gst.Event) bool {
	e.mu.Lock()

	self := gst.ToGstBin(instance)

	if e.Elem != nil {
		e.mu.Unlock()
		return pad.EventDefault(instance, event)
	}

	caps := event.ParseCaps()
	fc := e.selectFactory(caps)
	if fc == nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No factory found for caps: %q", caps.String()))
		e.mu.Unlock()
		return false
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Selected factory %s for caps %q", fc.Factory.GetName(), caps.String()))

	properties := make(map[string]interface{})
	maps.Copy(properties, e.Properties["*"])
	if factoryProperties, ok := e.Properties[fc.Factory.GetName()]; ok {
		maps.Copy(properties, factoryProperties)
	}

	elem, err := gst.NewElementWithProperties(fc.Factory.GetName(), properties)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create element from factory %s: %v", fc.Factory.GetName(), err))
		self.Error("Failed to create element from factory", err)
		e.mu.Unlock()
		return false
	}

	if err := self.Add(elem); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add element %s to bin: %v", elem.GetName(), err))
		self.Error("Failed to add element to bin", err)
		e.mu.Unlock()
		return false
	}

	e.Elem = elem

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Selected factory %s for caps %q", fc.Factory.GetName(), caps.String()))

	e.mu.Unlock()

	e.SinkPad.SetTarget(elem.GetStaticPad("sink"))
	e.SrcPad.SetTarget(elem.GetStaticPad("src"))

	if !elem.SyncStateWithParent() {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to sync state with parent for element %s", elem.GetName()))
	}

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Factory %s successfully configured for caps %q", fc.Factory.GetName(), caps.String()))

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
			query.SetCapsResult(resultCaps)
			return true
		case gst.QueryAcceptCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			acceptCaps := query.ParseAcceptCaps()
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
			query.SetCapsResult(resultCaps)
			return true
		case gst.QueryAcceptCaps:
			e := eweak.Value()
			if e == nil {
				return false
			}
			acceptCaps := query.ParseAcceptCaps()
			resultCaps := e.computeCaps(instance, pad, gst.PadDirectionSource, acceptCaps)
			accepted := resultCaps != nil && !resultCaps.IsEmpty()
			query.SetAcceptCapsResult(accepted)
			return true
		default:
			return pad.QueryDefault(instance, query)
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
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting factories property value: %v", err))
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
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Factory not found: %s", factoryName))
				continue
			}
			factories = append(factories, factory)
		}
		e.Factories = factories
	case "child-properties":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting child-properties property value: %v", err))
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
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid child property key: %s", k))
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
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GValue for factories property: %v", err))
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
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating GValue for selected-factory property: %v", err))
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
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Factory %s does not have both src and sink pads with always presence, skipping", c.Factory.GetName()))
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
