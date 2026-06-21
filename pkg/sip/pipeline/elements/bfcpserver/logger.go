package bfcpserver

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/samber/lo"
	"github.com/vopenia-io/bfcp"
)

func NewGstLogger(self *gst.Element) *GstLogger {
	return &GstLogger{
		wself: glib.WeakRefInit(self),
	}
}

type GstLogger struct {
	wself *glib.WeakRef
}

// Debugw implements [bfcp.Logger].
func (g *GstLogger) Debugw(msg string, keysAndValues ...interface{}) {
	fields := lo.Chunk(keysAndValues, 2)
	for _, kv := range fields {
		if len(kv) < 2 {
			continue
		}
		key, val := kv[0], kv[1]
		msg += fmt.Sprintf(" %s=%v", key, val)
	}

	self := gst.ToElement(g.wself.Get())
	if self == nil || self.Instance() == nil {
		CAT.Log(gst.LevelDebug, fmt.Sprintf("BFCPServer\nmsg=%s", msg))
	} else {
		self.Log(CAT, gst.LevelDebug, msg)
	}
}

// Errorw implements [bfcp.Logger].
func (g *GstLogger) Errorw(msg string, err error, keysAndValues ...interface{}) {
	fields := lo.Chunk(keysAndValues, 2)
	for _, kv := range fields {
		if len(kv) < 2 {
			continue
		}
		key, val := kv[0], kv[1]
		msg += fmt.Sprintf(" %s=%v", key, val)
	}

	msg += fmt.Sprintf(" error=%v", err)

	self := gst.ToElement(g.wself.Get())
	if self == nil || self.Instance() == nil {
		CAT.Log(gst.LevelError, fmt.Sprintf("BFCPServer\nmsg=%s", msg))
	} else {
		self.Log(CAT, gst.LevelError, msg)
	}
}

// Infow implements [bfcp.Logger].
func (g *GstLogger) Infow(msg string, keysAndValues ...interface{}) {
	fields := lo.Chunk(keysAndValues, 2)
	for _, kv := range fields {
		if len(kv) < 2 {
			continue
		}
		key, val := kv[0], kv[1]
		msg += fmt.Sprintf(" %s=%v", key, val)
	}

	self := gst.ToElement(g.wself.Get())
	if self == nil || self.Instance() == nil {
		CAT.Log(gst.LevelInfo, fmt.Sprintf("BFCPServer\nmsg=%s", msg))
	} else {
		self.Log(CAT, gst.LevelInfo, msg)
	}
}

// Warnw implements [bfcp.Logger].
func (g *GstLogger) Warnw(msg string, err error, keysAndValues ...interface{}) {
	fields := lo.Chunk(keysAndValues, 2)
	for _, kv := range fields {
		if len(kv) < 2 {
			continue
		}
		key, val := kv[0], kv[1]
		msg += fmt.Sprintf(" %s=%v", key, val)
	}

	msg += fmt.Sprintf(" error=%v", err)

	self := gst.ToElement(g.wself.Get())
	if self == nil || self.Instance() == nil {
		CAT.Log(gst.LevelWarning, fmt.Sprintf("BFCPServer\nmsg=%s", msg))
	} else {
		self.Log(CAT, gst.LevelWarning, msg)
	}
}

var _ bfcp.Logger = (*GstLogger)(nil)
