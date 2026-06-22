package sipbin

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func (f *sipBinFixture) emitAbort(t *testing.T) {
	t.Helper()
	if _, err := f.sipbin.Emit("abort-offer"); err != nil {
		t.Fatalf("failed to emit abort-offer signal: %v", err)
	}
}

func TestWaitReadyTimeout_Busy(t *testing.T) {
	tr := NewSipTransaction()

	unlock, err := tr.WaitReady()
	if err != nil {
		t.Fatalf("WaitReady failed: %v", err)
	}
	tr.SetPending(TransactionPendingKindAnswer)
	unlock() // pending answer keeps the transaction active

	if _, err := tr.WaitReadyTimeout(50 * time.Millisecond); err != ErrTransactionBusy {
		t.Fatalf("expected ErrTransactionBusy, got %v", err)
	}

	unlock, err = tr.Ack(TransactionPendingKindAnswer)
	if err != nil {
		t.Fatalf("Ack failed: %v", err)
	}
	unlock()

	unlock, err = tr.WaitReadyTimeout(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("expected transaction ready after ack, got %v", err)
	}
	unlock()
}

func TestTakeOverIfPending(t *testing.T) {
	// Nothing pending → no take-over.
	if NewSipTransaction().TakeOverIfPending(TransactionPendingKindAnswer) {
		t.Fatal("expected false when nothing is pending")
	}

	// Pending kind differs → no take-over.
	tr := NewSipTransaction()
	unlock, err := tr.WaitReady()
	if err != nil {
		t.Fatalf("WaitReady failed: %v", err)
	}
	tr.SetPending(TransactionPendingKindAck)
	unlock()
	if tr.TakeOverIfPending(TransactionPendingKindAnswer) {
		t.Fatal("expected false when pending kind differs")
	}

	// Pending Answer → take-over frees the transaction for an incoming offer.
	tr = NewSipTransaction()
	unlock, err = tr.WaitReady()
	if err != nil {
		t.Fatalf("WaitReady failed: %v", err)
	}
	tr.SetPending(TransactionPendingKindAnswer)
	unlock() // pending answer keeps the transaction active
	if !tr.TakeOverIfPending(TransactionPendingKindAnswer) {
		t.Fatal("expected take-over of pending Answer")
	}
	unlock, err = tr.WaitReadyTimeout(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("expected ready after take-over, got %v", err)
	}
	unlock()
}

// Device re-INVITE arriving while our early re-INVITE offer is unanswered (glare):
// the incoming offer must not block on the busy transaction. We yield our pending
// offer and answer the device's re-INVITE immediately, negotiating the slides
// stream. A late abort of our orphaned offer must then be a benign no-op.
func TestReInvite_GlareYieldsToDevice(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	// Short timeout so a regression to the busy path fails fast instead of in 5s.
	oldTimeout := offerReadyTimeout
	offerReadyTimeout = 200 * time.Millisecond
	defer func() { offerReadyTimeout = oldTimeout }()

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	// Initial offer with audio + camera + BFCP → triggers early re-INVITE
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	if answer := f.emitOffer(t, offer); answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	reInviteOffer := waitForOffer(t, ch, 5500*time.Millisecond)
	if msg := parseAnswer(t, reInviteOffer); msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in early re-INVITE offer, got %d", msg.MediasLen())
	}

	deviceOffer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
		"m=video 5006 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:slides\r\na=label:3",
	)

	// Our offer is pending; the device's re-INVITE must be answered immediately.
	answer := f.emitOffer(t, deviceOffer)
	if answer == "" {
		t.Fatal("expected non-empty answer for device re-INVITE during glare")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in answer, got %d", msg.MediasLen())
	}
	if msg.Media(3).GetPort() == 0 {
		t.Error("expected screenshare media accepted after yield (port > 0)")
	}
	if n := strings.Count(answer, "a=floorid:"); n != 1 {
		t.Errorf("expected exactly 1 floorid attribute on BFCP media, got %d", n)
	}

	// Our orphaned early re-INVITE eventually fails; the late abort must be a no-op.
	f.emitAbort(t)

	f.close()
}

// RFC 3264 §8: the o= line version must change between successive SDPs of the
// same session.
func TestSdpVersion_Increments(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1", "m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000")

	answer1 := f.emitOffer(t, offer)
	f.emitAck(t)
	answer2 := f.emitOffer(t, offer)
	f.emitAck(t)

	sess1, ver1 := parseOriginLine(t, answer1)
	sess2, ver2 := parseOriginLine(t, answer2)

	if sess1 != sess2 {
		t.Errorf("session id changed between answers: %q != %q", sess1, sess2)
	}
	if ver2 != ver1+1 {
		t.Errorf("expected o= version to increment from %d, got %d", ver1, ver2)
	}

	f.close()
}

func parseOriginLine(t *testing.T, sdp string) (sessID string, version int) {
	t.Helper()
	for _, line := range strings.Split(sdp, "\r\n") {
		if !strings.HasPrefix(line, "o=") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			t.Fatalf("malformed o= line: %q", line)
		}
		ver, err := strconv.Atoi(fields[2])
		if err != nil {
			t.Fatalf("malformed o= version in %q: %v", line, err)
		}
		return fields[1], ver
	}
	t.Fatalf("no o= line in SDP:\n%s", sdp)
	return "", 0
}
