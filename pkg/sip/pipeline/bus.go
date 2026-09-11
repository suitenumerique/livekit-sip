package pipeline

import (
	"fmt"
	"time"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/apperror"
)

func (p *Pipeline) SetupBus() {
	if p.bus != nil {
		p.pipeline.Log(CAT, gst.LevelError, "Bus already set up")
		return
	}

	p.bus = p.Pipeline().GetPipelineBus()
	p.pipeline.Log(CAT, gst.LevelDebug, "Setting bus to non-flushing")
	p.bus.SetFlushing(false)

	p.DumpDotLoop()

	pweak := weak.Make(p)
	if !p.bus.AddWatch(func(msg *gst.Message) bool {
		p := pweak.Value()
		if p == nil {
			fmt.Printf("Pipeline has been garbage collected, stopping bus watch\n")
			return false
		}
		success := p.onMessage(msg)
		return success
	}) {
		p.pipeline.Log(CAT, gst.LevelError, "Failed to add bus watch")
	}
}

func (p *Pipeline) CloseBus() {
	if p.bus == nil {
		p.pipeline.Log(CAT, gst.LevelWarning, "Bus not set up, cannot close")
		return
	}
	p.bus.SetFlushing(true)
	p.bus.RemoveWatch()
	p.bus = nil
}

func (p *Pipeline) onMessage(msg *gst.Message) bool {
	if p.Closed() {
		return true
	}
	pipeline := p.pipeline
	if pipeline == nil {
		return true
	}
	switch msg.Type() {
	case gst.MessageError:
		gErr := msg.ParseError()
		pipeline.Log(CAT, gst.LevelError, fmt.Sprintf("Pipeline error\nerr=%v\ndebug=%s", gErr, gErr.DebugString()))
		select {
		case p.dumpCH <- true:
		default:
		}
		if gErr.Domain() == apperror.AppErrorDomain.ToDomainQuark() && gErr.Code() == apperror.AppFatalError {
			// Leave the dump loop a moment to capture the failing graph before teardown.
			time.Sleep(500 * time.Millisecond)
			p.Close()
		}
	case gst.MessageLatency:
		pipeline.Log(CAT, gst.LevelDebug, "Pipeline latency changed")
		if !p.Pipeline().RecalculateLatency() {
			level := gst.LevelWarning
			if p.latencyWarned.Swap(true) {
				level = gst.LevelDebug
			}
			pipeline.Log(CAT, level, "Failed to recalculate pipeline latency")
		}
	case gst.MessageElement:
		structure := msg.GetStructure()
		if structure == nil {
			pipeline.Log(CAT, gst.LevelWarning, "Received element message with no structure")
			return true
		}
		switch name := structure.Name(); name {
		case "level":
			// Ignore level messages to avoid log spam
			return true
		case "dtmf-event":
			nbVal, err := structure.GetValue("number")
			if err != nil || nbVal == nil {
				pipeline.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received dtmf-event message with no number field\nnbVal=%v\nerr=%v", nbVal, err))
				return true
			}
			nb, ok := nbVal.(int)
			if !ok {
				pipeline.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received dtmf-event message with invalid number field\nnbValType=%T\nnbVal=%v", nbVal, nbVal))
				return true
			}
			pipeline.Log(CAT, gst.LevelDebug, fmt.Sprintf("Received dtmf-event message\nnumber=%d", nb))
			p.dtmfCh <- nb
			return true
		default:
			pipeline.Log(CAT, gst.LevelDebug, fmt.Sprintf("Received element message\nname=%s\nstructure=%s", name, structure.String()))
			return true
		}
	case gst.MessageStateChanged:
		select {
		case p.dumpCH <- false:
		default:
		}
	case gst.MessageQoS:
		p.logQoS(msg)
	default:
		pipeline.Log(CAT, gst.LevelTrace, fmt.Sprintf("Unhandled bus message\ntype=%v", msg.Type()))
	}
	return true
}

// logQoS reports buffers dropped by an element that posts QoS messages, at
// most once per second per source; the counters are cumulative.
func (p *Pipeline) logQoS(msg *gst.Message) {
	src := msg.Source()
	now := time.Now()
	p.qosMu.Lock()
	if now.Sub(p.qosLast[src]) < time.Second {
		p.qosMu.Unlock()
		return
	}
	p.qosLast[src] = now
	p.qosMu.Unlock()

	values := msg.ParseQoS()
	processed, dropped := "?", "?"
	if st := msg.GetStructure(); st != nil {
		if v, err := st.GetValue("processed"); err == nil {
			processed = fmt.Sprint(v)
		}
		if v, err := st.GetValue("dropped"); err == nil {
			dropped = fmt.Sprint(v)
		}
	}
	p.pipeline.Log(CAT, gst.LevelWarning, fmt.Sprintf("Buffers dropped\nsource=%s\nlive=%t\nrunning_time=%s\nprocessed=%s\ndropped=%s", src, values.Live, values.RunningTime, processed, dropped))
}

func (p *Pipeline) DTMF() chan int {
	return p.dtmfCh
}
