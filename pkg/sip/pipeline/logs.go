package pipeline

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/config"
)

var QLogSipCallID = glib.QuarkFromString("livekit-sip-log-sipcallid")
var gstLogger logger.Logger

func SetupLogging(log logger.Logger, gstConf config.GstConfig) {

	if gstConf.Debug != "-" {
		gst.SetDebugThresholdFromString(gstConf.Debug, false)
		log.Infow("Gst debug logging enabled", "debug", gstConf.Debug)
	}

	if os.Getenv("GST_FORCE_DEFAULT_LOGGER") == "1" {
		return
	}
	gstLogger = log.WithComponent("gst")
	gst.SetLogFunction(gstLogFunc)
}

func (p *Pipeline) SetLogHandler() {
	p.pipeline.Connect("deep-element-added", func(_ any, _ any, child *gst.Element) {
		if child == nil {
			return
		}
		child.SetQDataQuark(QLogSipCallID, p.sipCallID)
	})
}

func gstLogFunc(
	category *gst.DebugCategory,
	level gst.DebugLevel,
	file string,
	function string,
	line int,
	object *gst.LoggedObject,
	message *gst.DebugMessage,
) {
	sipCallID := ""
	objectName := message.GetId()
	if object != nil && glib.GIsObject(object.Get()) {
		gobj := glib.NewObject(glib.ToGObject(object.Get()))
		if gobj != nil {
			if val := gobj.GetQDataQuark(QLogSipCallID); val != nil {
				sipCallID = val.(string)
			}
		}
	}

	log := gstLogger.WithComponent(category.GetName()).WithValues("sipCallID", sipCallID, "loc", fmt.Sprintf("%s:%d:%s", file, line, function), "object", objectName)
	msg := strings.Split(message.Get(), "\n")
	if len(msg) == 0 {
		msg = []string{message.Get()}
	}
	if len(msg) > 1 {
		log = log.WithValues("details", strings.Join(msg[1:], "\n"))
	}

	switch level {
	case gst.LevelError:
		log.Errorw(msg[0], nil)
	case gst.LevelWarning:
		log.Warnw(msg[0], nil)
	case gst.LevelInfo:
		log.Infow(msg[0])
	case gst.LevelDebug:
		log.Debugw(msg[0])
	default:
		log.Infow(msg[0])
	}
}
