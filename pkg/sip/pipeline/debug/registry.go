//go:build debug_ui

package debug

import (
	"net/http"

	"github.com/go-gst/go-gst/gst"
)

var registry = map[string]func(*gst.Element) http.Handler{}

func Register(factoryName string, factory func(*gst.Element) http.Handler) {
	registry[factoryName] = factory
}
