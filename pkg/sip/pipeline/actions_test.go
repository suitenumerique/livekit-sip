package pipeline

import (
	"errors"
	"testing"
)

func TestEmitOnClosedPipelineReturnsError(t *testing.T) {
	p := &Pipeline{SipIo: &SipIo{}}
	p.closed.Break()

	if err := p.EmitAckSDP(""); !errors.Is(err, ErrPipelineClosed) {
		t.Fatalf("EmitAckSDP: got %v, want ErrPipelineClosed", err)
	}
	if err := p.EmitAnswerSDP(""); !errors.Is(err, ErrPipelineClosed) {
		t.Fatalf("EmitAnswerSDP: got %v, want ErrPipelineClosed", err)
	}
	if err := p.EmitOfferAborted(); !errors.Is(err, ErrPipelineClosed) {
		t.Fatalf("EmitOfferAborted: got %v, want ErrPipelineClosed", err)
	}
	if _, err := p.EmitOfferSDP(""); !errors.Is(err, ErrPipelineClosed) {
		t.Fatalf("EmitOfferSDP: got %v, want ErrPipelineClosed", err)
	}
	if _, err := p.EmitCreateOfferSDP(); !errors.Is(err, ErrPipelineClosed) {
		t.Fatalf("EmitCreateOfferSDP: got %v, want ErrPipelineClosed", err)
	}
}

func TestEmitWithoutSipBinReturnsError(t *testing.T) {
	for name, p := range map[string]*Pipeline{
		"nil sip io":  {},
		"nil sip bin": {SipIo: &SipIo{}},
	} {
		if err := p.EmitAckSDP(""); !errors.Is(err, ErrPipelineClosed) {
			t.Fatalf("%s: got %v, want ErrPipelineClosed", name, err)
		}
	}
}
