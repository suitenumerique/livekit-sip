package sourcereader

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
)

func Register() bool {
	return gst.RegisterElement(
		// no plugin:
		nil,
		// The name of the element
		"sourcereader",
		// The rank of the element
		gst.RankNone,
		// The GoElement implementation for the element
		&SourceReader{},
		// The base subclass this element extends
		base.ExtendsBaseSrc,
	)
}
