package pipeline

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/sip/pkg/config"
)

var QLogSipCallID = glib.QuarkFromString("livekit-sip-log-sipcallid")
var gstLogger logger.Logger
var logDeduplication = sync.Map{}
var gstLogDedup = false
var gstLogDedupDuration = 100 * time.Millisecond

type gstLogKey = uint64
type gstLogValue struct {
	level      gst.DebugLevel
	sipCallID  string
	cat        string
	loc        string
	objectName string
	msg        string
	details    string
	count      atomic.Int64
	at         int64
}

const (
	// https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function#FNV_prime
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

func fnvAdd(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

func SetupLogging(log logger.Logger, gstConf config.GstConfig) {

	if gstConf.Debug != "-" {
		gst.SetDebugThresholdFromString(gstConf.Debug, false)
		log.Infow("Gst debug logging enabled", "debug", gstConf.Debug)
	}

	if os.Getenv("GST_FORCE_DEFAULT_LOGGER") == "1" {
		return
	}
	gstLogDedup = gstConf.DedupLogs
	gstLogger = log.WithComponent("gst")
	gst.SetLogFunction(gstLogFunc)

	if gstLogDedup {
		go LogDeduplicationGC()
	}
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
	loc := fmt.Sprintf("%s:%d:%s", file, line, function)
	cat := category.GetName()
	data := strings.Split(message.Get(), "\n")
	var msg, details string
	if len(data) == 0 {
		msg = ""
	} else {
		msg = data[0]
	}
	if len(data) > 1 {
		details = strings.Join(data[1:], "\n")
	}

	if gstLogDedup {
		key := gstLogKey(fnvOffset64)
		key ^= uint64(level)
		key *= fnvPrime64
		key = fnvAdd(key, sipCallID)
		key = fnvAdd(key, "\x00")
		key = fnvAdd(key, cat)
		key = fnvAdd(key, "\x00")
		key = fnvAdd(key, loc)
		key = fnvAdd(key, "\x00")
		key = fnvAdd(key, objectName)
		key = fnvAdd(key, "\x00")
		key = fnvAdd(key, msg)
		key = fnvAdd(key, "\x00")
		key = fnvAdd(key, details)
		now := time.Now()
		if val, ok := logDeduplication.Load(key); ok {
			v := val.(*gstLogValue)
			v.count.Add(1)
			v.at = int64(now.UnixNano())
			logDeduplication.Store(key, v)
			return
		}
		v := &gstLogValue{
			level:      level,
			sipCallID:  sipCallID,
			cat:        cat,
			loc:        loc,
			objectName: objectName,
			msg:        msg,
			details:    details,
			at:         int64(now.UnixNano()),
		}
		if val, ok := logDeduplication.LoadOrStore(key, v); ok {
			v := val.(*gstLogValue)
			v.count.Add(1)
			v.at = int64(now.UnixNano())
			logDeduplication.Store(key, v)
			return
		}
	}

	gstLogPrint(&gstLogValue{
		level:      level,
		sipCallID:  sipCallID,
		cat:        cat,
		loc:        loc,
		objectName: objectName,
		msg:        msg,
		details:    details,
		at:         int64(time.Now().UnixNano()),
	})
}

func gstLogPrint(data *gstLogValue) {
	log := gstLogger.WithComponent(data.cat).WithValues("sipCallID", data.sipCallID, "loc", data.loc)
	if data.objectName != "" {
		log = log.WithValues("object", data.objectName)
	}
	if data.details != "" {
		log = log.WithValues("details", data.details)
	}
	count := data.count.Load()
	if count > 0 {
		log = log.WithValues("count", count+1)
	}

	switch data.level {
	case gst.LevelError:
		log.Errorw(data.msg, nil)
	case gst.LevelWarning:
		log.Warnw(data.msg, nil)
	case gst.LevelInfo:
		log.Infow(data.msg)
	case gst.LevelDebug:
		log.Debugw(data.msg)
	default:
		log.Infow(data.msg)
	}
}

func LogDeduplicationGC() {
	ticker := time.NewTicker(gstLogDedupDuration)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		logDeduplication.Range(func(key, value any) bool {
			v := value.(*gstLogValue)
			count := v.count.Load()
			if time.Unix(0, v.at).Add(gstLogDedupDuration).Before(now) {
				logDeduplication.Delete(key)
				if count > 0 {
					gstLogPrint(v)
				}
			} else if count >= 999 {
				gstLogPrint(v)
				v.count.Store(0)
			}
			return true
		})
	}
}
