package pipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
)

func LinkPad(src, dst *gst.Pad) error {
	if src == nil {
		return fmt.Errorf("source pad is nil")
	}
	if dst == nil {
		return fmt.Errorf("destination pad is nil")
	}
	if r := src.Link(dst); r != gst.PadLinkOK {
		return fmt.Errorf("failed to link pads: %s", r.String())
	}
	return nil
}

func ReleasePad(pad *gst.Pad) {
	if pad != nil {
		parent := pad.GetParentElement()
		if parent != nil {
			parent.ReleaseRequestPad(pad)
		}
	}
}

func CastErr[T any](v any, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	casted, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("failed to cast value")
	}
	return casted, nil
}

func ValidatePad(pad *gst.Pad) error {
	if pad == nil {
		return fmt.Errorf("pad is nil")
	}
	parent := pad.GetParentElement()
	if parent == nil {
		return fmt.Errorf("pad's parent element is nil")
	}
	return nil
}
