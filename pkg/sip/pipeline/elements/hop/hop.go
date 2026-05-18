package hop

/*
#cgo pkg-config: gstreamer-1.0

#include <gst/gst.h>

#include "hop_src.h"
#include "hop_sink.h"
#include "hop_link.h"
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/go-gst/go-gst/gst"
)

func Register() bool {
	if int(C.gst_element_register(nil, C.CString("hopsink"), C.GST_RANK_NONE, C.HOP_TYPE_SINK)) == 0 {
		return false
	}
	if int(C.gst_element_register(nil, C.CString("hopsrc"), C.GST_RANK_NONE, C.HOP_TYPE_SRC)) == 0 {
		return false
	}
	return true
}

// NewPair creates a paired hopsink + hopsrc and binds them so events,
// queries, and buffers flow across the pair. The returned elements are
// not yet added to a pipeline — caller is responsible for AddMany() and
// SyncStateWithParent().
func NewPair() (sink, src *gst.Element, err error) {
	src, err = gst.NewElement("hopsrc")
	if err != nil {
		return nil, nil, fmt.Errorf("create hopsrc: %w", err)
	}
	sink, err = gst.NewElement("hopsink")
	if err != nil {
		return nil, nil, fmt.Errorf("create hopsink: %w", err)
	}
	if C.hop_pair_bind(
		(*C.GstElement)(unsafe.Pointer(sink.Instance())),
		(*C.GstElement)(unsafe.Pointer(src.Instance())),
	) == 0 {
		return nil, nil, fmt.Errorf("hop_pair_bind: type mismatch")
	}
	return sink, src, nil
}
