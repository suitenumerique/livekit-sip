package pipeline

import (
	"errors"
	"fmt"
	"time"
	"weak"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/apperror"
)

func (p *Pipeline) SetupBus() {
	if p.bus != nil {
		p.Log.Errorw("Bus already set up", errors.New("bus already set up"))
		return
	}

	p.bus = p.Pipeline().GetPipelineBus()
	p.Log.Debugw("Setting bus to non-flushing")
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
		p.Log.Errorw("Failed to add bus watch", errors.New("bus watch add failed"))
	}
}

func (p *Pipeline) CloseBus() {
	if p.bus == nil {
		p.Log.Warnw("Bus not set up, cannot close", nil)
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
	switch msg.Type() {
	case gst.MessageError:
		gErr := msg.ParseError()
		p.Log.Errorw("Pipeline error", gErr, "debug", gErr.DebugString())
		p.dumpCH <- true
		time.Sleep(500 * time.Millisecond)
		if gErr.Domain() == apperror.AppErrorDomain.ToDomainQuark() && gErr.Code() == apperror.AppFatalError {
			p.Close()
		}
	case gst.MessageLatency:
		p.Log.Debugw("Pipeline latency changed")
		if !p.Pipeline().RecalculateLatency() {
			p.Log.Warnw("Failed to recalculate pipeline latency", nil)
		}
	case gst.MessageElement:
		structure := msg.GetStructure()
		if structure == nil {
			p.Log.Warnw("Received element message with no structure", nil)
			return true
		}
		switch name := structure.Name(); name {
		case "level":
			// Ignore level messages to avoid log spam
			return true
		case "dtmf-event":
			nbVal, err := structure.GetValue("number")
			if err != nil || nbVal == nil {
				p.Log.Warnw("Received dtmf-event message with no number field", err, "nbVal", nbVal)
				return true
			}
			nb, ok := nbVal.(int)
			if !ok {
				p.Log.Warnw("Received dtmf-event message with invalid number field", nil, "nbVal", fmt.Sprintf("%T=%v", nbVal, nbVal))
				return true
			}
			p.Log.Debugw("Received dtmf-event message", "number", nb)
			p.dtmfCh <- nb
			return true
		default:
			p.Log.Debugw("Received element message", "name", name, "structure", structure.String())
			return true
		}
	case gst.MessageStateChanged:
		select {
		case p.dumpCH <- false:
		default:
		}
	default:
		p.Log.Debugw("Unhandled bus message", "type", msg.Type())
	}
	return true
}

func (p *Pipeline) DTMF() chan int {
	return p.dtmfCh
}
