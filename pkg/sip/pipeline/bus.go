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
		p.dumpCH <- true
		time.Sleep(500 * time.Millisecond)
		if gErr.Domain() == apperror.AppErrorDomain.ToDomainQuark() && gErr.Code() == apperror.AppFatalError {
			p.Close()
		}
	case gst.MessageLatency:
		pipeline.Log(CAT, gst.LevelDebug, "Pipeline latency changed")
		if !p.Pipeline().RecalculateLatency() {
			pipeline.Log(CAT, gst.LevelWarning, "Failed to recalculate pipeline latency")
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
	default:
		pipeline.Log(CAT, gst.LevelTrace, fmt.Sprintf("Unhandled bus message\ntype=%v", msg.Type()))
	}
	return true
}

func (p *Pipeline) DTMF() chan int {
	return p.dtmfCh
}
