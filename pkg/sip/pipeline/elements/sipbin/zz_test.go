package sipbin

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/bfcpserver"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/testutils"
)

func TestMain(m *testing.M) {
	glib.SetEnv("GST_DEBUG", "sipbin:5,GST_TRACER:7", true)
	gst.Init(nil)
	Register()
	bfcpserver.Register()
	os.Exit(m.Run())
}

func makeSDP(sessionIP string, medias ...string) string {
	lines := []string{
		"v=0",
		"o=- 123456 1 IN IP4 " + sessionIP,
		"s=Test",
		"t=0 0",
		"c=IN IP4 " + sessionIP,
	}
	for _, m := range medias {
		lines = append(lines, m)
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func pcmuCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMU,clock-rate=8000")
}

func h264Caps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000")
}

func opusCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000")
}

func testDebugDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("testdata", t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create debug dir %s: %v", dir, err)
	}
	return dir
}

func dumpDot(t *testing.T, pipeline *gst.Pipeline, label string) {
	t.Helper()
	dir := testDebugDir(t)
	data := pipeline.Bin.DebugBinToDotData(gst.DebugGraphShowVerbose)
	path := filepath.Join(dir, label+".dot")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Logf("WARNING: failed to write dot file %s: %v", path, err)
	}
}

type sipBinFixture struct {
	t        *testing.T
	pipeline *gst.Pipeline
	sipbin   *gst.Element
}

func newFixture(t *testing.T, formats []*gst.Caps) *sipBinFixture {
	t.Helper()

	pipeline, err := gst.NewPipeline("test-pipeline")
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	sipbin, err := gst.NewElement("sipbin")
	if err != nil {
		t.Fatalf("failed to create sipbin element: %v", err)
	}

	if err := pipeline.Add(sipbin); err != nil {
		t.Fatalf("failed to add sipbin to pipeline: %v", err)
	}

	if err := sipbin.SetProperty("ip", "127.0.0.1"); err != nil {
		t.Fatalf("failed to set ip property: %v", err)
	}

	if len(formats) > 0 {
		items := make([]interface{}, len(formats))
		for i, c := range formats {
			items[i] = c
		}
		arr, err := glib.NewArray(items)
		if err != nil {
			t.Fatalf("failed to create formats array: %v", err)
		}
		if err := sipbin.SetProperty("formats", arr); err != nil {
			t.Fatalf("failed to set formats property: %v", err)
		}
	}

	// NULL → READY
	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatalf("failed to set pipeline to READY: %v", err)
	}

	return &sipBinFixture{t: t, pipeline: pipeline, sipbin: sipbin}
}

func (f *sipBinFixture) emitOffer(t *testing.T, offer string) string {
	t.Helper()
	t.Logf("Offer SDP:\n%s", offer)
	result, err := f.sipbin.Emit("offer-sdp", offer)
	if err != nil {
		t.Fatalf("failed to emit offer-sdp signal: %v", err)
	}
	answer, ok := result.(string)
	if !ok {
		t.Fatalf("offer-sdp signal returned unexpected type: %T", result)
	}
	t.Logf("Answer SDP:\n%s", answer)
	return answer
}

func (f *sipBinFixture) emitAck(t *testing.T) {
	t.Helper()
	if _, err := f.sipbin.Emit("ack-sdp", ""); err != nil {
		t.Fatalf("failed to emit ack-sdp signal: %v", err)
	}
}

func (f *sipBinFixture) emitAckWithSDP(t *testing.T, sdp string) {
	t.Helper()
	t.Logf("ACK SDP:\n%s", sdp)
	if _, err := f.sipbin.Emit("ack-sdp", sdp); err != nil {
		t.Fatalf("failed to emit ack-sdp signal with SDP: %v", err)
	}
}

func (f *sipBinFixture) close() {
	dumpDot(f.t, f.pipeline, "before_cleanup")
	// READY → NULL
	if err := f.pipeline.SetState(gst.StateNull); err != nil {
		f.t.Errorf("failed to set pipeline to NULL: %v", err)
	}
	f.pipeline = nil
	f.sipbin = nil
}

func parseAnswer(t *testing.T, answer string) *gstsdp.Message {
	t.Helper()
	msg, err := gstsdp.ParseSDPMessage(answer)
	if err != nil {
		t.Fatalf("failed to parse answer SDP: %v\nAnswer:\n%s", err, answer)
	}
	return msg
}

// Format caps without fixed payload type, for matching any PT in vendor SDPs.
func pcmuAnyCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMU,clock-rate=8000")
}

func pcmaAnyCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=PCMA,clock-rate=8000")
}

func g722AnyCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=G722,clock-rate=8000")
}

func h264AnyCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000")
}

func telephoneEventAnyCaps() *gst.Caps {
	return gst.NewCapsFromString("application/x-rtp,media=audio,encoding-name=TELEPHONE-EVENT,clock-rate=8000")
}

// Real-world SDP offers from vendor devices.
var ciscoRoomKitOffer = strings.Join([]string{
	"v=0",
	"o=CiscoSystemsCCM-SIP 277 1 IN IP4 172.16.100.11",
	"s=SIP Call",
	"c=IN IP4 172.16.100.101",
	"b=AS:4096",
	"t=0 0",
	"m=audio 24068 RTP/AVP 9 0 8 18 101",
	"a=rtpmap:9 G722/8000",
	"a=rtpmap:0 PCMU/8000",
	"a=rtpmap:8 PCMA/8000",
	"a=rtpmap:18 G729/8000",
	"a=fmtp:18 annexb=no",
	"a=rtpmap:101 telephone-event/8000",
	"a=fmtp:101 0-15",
	"a=sendrecv",
	"m=video 27882 RTP/AVP 126 97",
	"b=TIAS:2000000",
	"a=rtpmap:126 H264/90000",
	"a=fmtp:126 profile-level-id=42E01F;packetization-mode=1;max-fs=3600;max-mbps=108000;max-rcmd-nalu-size=32000;level-asymmetry-allowed=1",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=640C1F;packetization-mode=1;max-fs=3600;max-mbps=108000;level-asymmetry-allowed=1",
	"a=content:main",
	"a=label:11",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=video 28052 RTP/AVP 126 97",
	"b=TIAS:2000000",
	"a=rtpmap:126 H264/90000",
	"a=fmtp:126 profile-level-id=42E01F;packetization-mode=1;max-fs=8160;max-mbps=244800;max-rcmd-nalu-size=32000;level-asymmetry-allowed=1",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=640C1F;packetization-mode=1;max-fs=8160;max-mbps=244800;level-asymmetry-allowed=1",
	"a=content:slides",
	"a=label:12",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=application 5748 UDP/BFCP *",
	"a=floorctrl:c-s",
	"a=confid:1",
	"a=userid:2",
	"a=floorid:2 mstrm:12",
	"a=bfcpver:1",
	"m=application 33516 RTP/AVP 125",
	"a=rtpmap:125 H224/4800",
	"a=sendrecv",
	"",
}, "\r\n")

var polyStudioXOffer = strings.Join([]string{
	"v=0",
	"o=Opal 1234 5678 IN IP4 10.0.1.50",
	"s=SIP Call",
	"c=IN IP4 10.0.1.50",
	"b=AS:4096",
	"t=0 0",
	"m=audio 20000 RTP/AVP 115 9 0 8 101",
	"a=rtpmap:115 G7221/16000",
	"a=fmtp:115 bitrate=32000",
	"a=rtpmap:9 G722/8000",
	"a=rtpmap:0 PCMU/8000",
	"a=rtpmap:8 PCMA/8000",
	"a=rtpmap:101 telephone-event/8000",
	"a=fmtp:101 0-15",
	"a=sendrecv",
	"m=video 22300 RTP/AVP 99 97 126",
	"b=TIAS:2000000",
	"a=rtpmap:99 H264/90000",
	"a=fmtp:99 profile-level-id=640032;packetization-mode=1;max-fs=8160;max-mbps=244800;level-asymmetry-allowed=1",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=3600;max-mbps=108000;level-asymmetry-allowed=1",
	"a=rtpmap:126 H264/90000",
	"a=fmtp:126 profile-level-id=42E00D;packetization-mode=1;max-fs=396;max-mbps=11880",
	"a=content:main",
	"a=label:11",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=video 20452 RTP/AVP 97 126 96 34",
	"b=TIAS:2000000",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=8160;max-mbps=244800;level-asymmetry-allowed=1",
	"a=rtpmap:126 H264/90000",
	"a=fmtp:126 profile-level-id=42E00D;packetization-mode=1;max-fs=3600;max-mbps=108000",
	"a=rtpmap:96 H263-1998/90000",
	"a=fmtp:96 CIF=1;CIF4=1;QCIF=1",
	"a=rtpmap:34 H263/90000",
	"a=fmtp:34 CIF=1;QCIF=1",
	"a=content:slides",
	"a=label:12",
	"a=sendrecv",
	"m=application 29299 UDP/BFCP *",
	"a=floorctrl:c-s",
	"a=confid:1",
	"a=userid:8",
	"a=floorid:2 mstrm:12",
	"a=bfcpver:1",
	"",
}, "\r\n")

var polyLegacyOffer = strings.Join([]string{
	"v=0",
	"o=Opal 9876 1 IN IP4 10.0.2.30",
	"s=SIP Call",
	"c=IN IP4 10.0.2.30",
	"b=AS:2048",
	"t=0 0",
	"m=audio 20000 RTP/AVP 115 9 0 8 101",
	"a=rtpmap:115 G7221/16000",
	"a=fmtp:115 bitrate=32000",
	"a=rtpmap:9 G722/8000",
	"a=rtpmap:0 PCMU/8000",
	"a=rtpmap:8 PCMA/8000",
	"a=rtpmap:101 telephone-event/8000",
	"a=fmtp:101 0-15",
	"a=sendrecv",
	"m=video 22300 RTP/AVP 97",
	"b=TIAS:1500000",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=3600;max-mbps=108000;level-asymmetry-allowed=1",
	"a=content:main",
	"a=label:11",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=application 29299 UDP/BFCP *",
	"a=floorctrl:c-s",
	"a=confid:1",
	"a=userid:8",
	"a=floorid:2 mstrm:12",
	"a=bfcpver:1",
	"",
}, "\r\n")

var yealinkVC800Offer = strings.Join([]string{
	"v=0",
	"o=yealink 5432 1 IN IP4 192.168.1.100",
	"s=SIP Call",
	"c=IN IP4 192.168.1.100",
	"b=AS:4096",
	"t=0 0",
	"m=audio 10000 RTP/AVP 9 0 8 101",
	"a=rtpmap:9 G722/8000",
	"a=rtpmap:0 PCMU/8000",
	"a=rtpmap:8 PCMA/8000",
	"a=rtpmap:101 telephone-event/8000",
	"a=fmtp:101 0-15",
	"a=sendrecv",
	"m=video 10002 RTP/AVP 97 126",
	"b=TIAS:2000000",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=3600;max-mbps=108000;level-asymmetry-allowed=1",
	"a=rtpmap:126 H264/90000",
	"a=fmtp:126 profile-level-id=640032;packetization-mode=1;max-fs=3600;max-mbps=108000;level-asymmetry-allowed=1",
	"a=content:main",
	"a=label:11",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=video 10004 RTP/AVP 97 126",
	"b=TIAS:2000000",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=8160;max-mbps=244800;level-asymmetry-allowed=1",
	"a=rtpmap:126 H264/90000",
	"a=fmtp:126 profile-level-id=640032;packetization-mode=1;max-fs=8160;max-mbps=244800;level-asymmetry-allowed=1",
	"a=content:slides",
	"a=label:12",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=application 10006 UDP/BFCP *",
	"a=floorctrl:c-s",
	"a=confid:1",
	"a=userid:4",
	"a=floorid:2 mstrm:12",
	"a=bfcpver:1",
	"",
}, "\r\n")

func TestNegotiate_AudioOnly(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1", "m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000")
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	media := msg.Media(0)
	if media.GetMedia() != "audio" {
		t.Errorf("expected media type 'audio', got '%s'", media.GetMedia())
	}
	if media.GetPort() == 0 {
		t.Error("expected audio port > 0, got 0")
	}

	f.close()
}

func TestNegotiate_VideoOnly(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{h264Caps()})

	offer := makeSDP("192.168.1.1", "m=video 5000 RTP/AVP 120\r\na=rtpmap:120 H264/90000")
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	media := msg.Media(0)
	if media.GetMedia() != "video" {
		t.Errorf("expected media type 'video', got '%s'", media.GetMedia())
	}
	if media.GetPort() == 0 {
		t.Error("expected video port > 0, got 0")
	}

	f.close()
}

func TestNegotiate_AudioAndVideo(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 2 {
		t.Fatalf("expected 2 medias in answer, got %d", msg.MediasLen())
	}

	audio := msg.Media(0)
	if audio.GetMedia() != "audio" {
		t.Errorf("expected first media type 'audio', got '%s'", audio.GetMedia())
	}
	if audio.GetPort() == 0 {
		t.Error("expected audio port > 0, got 0")
	}

	video := msg.Media(1)
	if video.GetMedia() != "video" {
		t.Errorf("expected second media type 'video', got '%s'", video.GetMedia())
	}
	if video.GetPort() == 0 {
		t.Error("expected video port > 0, got 0")
	}

	f.close()
}

func TestNegotiate_UnsupportedMediaType(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1", "m=text 5000 RTP/AVP 100\r\na=rtpmap:100 t140/1000")
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	media := msg.Media(0)
	if media.GetPort() != 0 {
		t.Errorf("expected disabled media (port 0), got port %d", media.GetPort())
	}

	f.close()
}

func TestNegotiate_VideoScreenShare(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{h264Caps()})

	offer := makeSDP("192.168.1.1", "m=video 5000 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:slides")
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	media := msg.Media(0)
	if media.GetMedia() != "video" {
		t.Errorf("expected media type 'video', got '%s'", media.GetMedia())
	}
	if media.GetPort() == 0 {
		t.Error("expected screen share video port > 0, got 0")
	}

	f.close()
}

func TestNegotiate_NoMatchingFormats(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{opusCaps()})

	offer := makeSDP("192.168.1.1", "m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000")
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	media := msg.Media(0)
	if media.GetPort() != 0 {
		t.Errorf("expected disabled media (port 0) for non-matching format, got port %d", media.GetPort())
	}

	f.close()
}

func TestNegotiate_NoIP(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	pipeline, err := gst.NewPipeline("test-pipeline")
	if err != nil {
		t.Fatalf("failed to create pipeline: %v", err)
	}

	sipbin, err := gst.NewElement("sipbin")
	if err != nil {
		t.Fatalf("failed to create sipbin element: %v", err)
	}

	if err := pipeline.Add(sipbin); err != nil {
		t.Fatalf("failed to add sipbin to pipeline: %v", err)
	}

	if err := pipeline.SetState(gst.StateReady); err != nil {
		t.Fatalf("failed to set pipeline to READY: %v", err)
	}

	offer := makeSDP("192.168.1.1", "m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000")
	t.Logf("Offer SDP:\n%s", offer)
	result, err := sipbin.Emit("offer-sdp", offer)
	if err != nil {
		t.Fatalf("failed to emit offer-sdp signal: %v", err)
	}
	answer, _ := result.(string)
	t.Logf("Answer SDP:\n%s", answer)
	if answer != "" {
		t.Errorf("expected empty answer when no IP is configured, got: %s", answer)
	}

	dumpDot(t, pipeline, "before_cleanup")
	if err := pipeline.SetState(gst.StateNull); err != nil {
		t.Errorf("failed to set pipeline to NULL: %v", err)
	}
	pipeline = nil
	sipbin = nil
}

func TestNegotiate_MediaCountMatches(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 3 {
		t.Fatalf("expected answer media count to match offer (3), got %d", msg.MediasLen())
	}

	if msg.Media(0).GetPort() == 0 {
		t.Error("expected audio port > 0")
	}
	if msg.Media(1).GetPort() != 0 {
		t.Errorf("expected video disabled (port 0), got port %d", msg.Media(1).GetPort())
	}
	// BFCP: accepted
	if msg.Media(2).GetPort() == 0 {
		t.Error("expected BFCP port > 0")
	}

	f.close()
}

func TestNegotiate_DuplicateMediaKind(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=audio 5002 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 2 {
		t.Fatalf("expected 2 medias in answer, got %d", msg.MediasLen())
	}

	if msg.Media(0).GetPort() == 0 {
		t.Error("expected first audio port > 0, got 0")
	}
	if msg.Media(1).GetPort() != 0 {
		t.Errorf("expected second audio disabled (port 0), got port %d", msg.Media(1).GetPort())
	}

	f.close()
}

func TestNegotiate_TelephoneEvent(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuAnyCaps(), telephoneEventAnyCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-15",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	audio := msg.Media(0)
	if audio.GetMedia() != "audio" {
		t.Errorf("expected media type 'audio', got '%s'", audio.GetMedia())
	}
	if audio.GetPort() == 0 {
		t.Error("expected audio port > 0, got 0")
	}

	// The merged caps selection should include both PCMU and telephone-event
	// formats in the answer, not just the first compatible format.
	hasPCMU := false
	hasTelephoneEvent := false
	for _, format := range audio.Formats() {
		pt, err := strconv.Atoi(format)
		if err != nil {
			continue
		}
		caps, err := audio.GetCaps(pt)
		if err != nil || caps.GetSize() == 0 {
			continue
		}
		encoding, err := caps.GetStructureAt(0).GetString("encoding-name")
		if err != nil {
			continue
		}
		switch encoding {
		case "PCMU":
			hasPCMU = true
		case "TELEPHONE-EVENT":
			hasTelephoneEvent = true
		}
	}
	if !hasPCMU {
		t.Error("expected PCMU in answer formats")
	}
	if !hasTelephoneEvent {
		t.Error("expected TELEPHONE-EVENT in answer formats")
	}

	f.close()
}

func TestNegotiate_TelephoneEvent_NoMatchWithoutFormat(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	// Only PCMU configured, no telephone-event format — telephone-event
	// should NOT appear in the answer.
	f := newFixture(t, []*gst.Caps{pcmuAnyCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-15",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	audio := msg.Media(0)
	for _, format := range audio.Formats() {
		pt, err := strconv.Atoi(format)
		if err != nil {
			continue
		}
		caps, err := audio.GetCaps(pt)
		if err != nil || caps.GetSize() == 0 {
			continue
		}
		encoding, err := caps.GetStructureAt(0).GetString("encoding-name")
		if err != nil {
			continue
		}
		if encoding == "TELEPHONE-EVENT" {
			t.Error("telephone-event should not be in answer when not in configured formats")
		}
	}

	f.close()
}

func TestNegotiate_BFCP(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1", "m=application 5000 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1")
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 1 {
		t.Fatalf("expected 1 media in answer, got %d", msg.MediasLen())
	}

	media := msg.Media(0)
	if media.GetMedia() != "application" {
		t.Errorf("expected media type 'application', got '%s'", media.GetMedia())
	}
	if media.GetPort() == 0 {
		t.Error("expected BFCP port > 0, got 0")
	}
	if media.GetProto() != "UDP/BFCP" {
		t.Errorf("expected proto 'UDP/BFCP', got '%s'", media.GetProto())
	}

	f.close()
}

// Cisco Room Kit: audio + video(main) + video(slides) + BFCP + H.224(FECC)
// Expect: audio accepted, main video accepted, slides video accepted, BFCP disabled, H.224 disabled
func TestNegotiate_CiscoRoomKit(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{g722AnyCaps(), pcmuAnyCaps(), pcmaAnyCaps(), h264AnyCaps()})

	answer := f.emitOffer(t, ciscoRoomKitOffer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 5 {
		t.Fatalf("expected 5 medias in answer (audio, video main, video slides, BFCP, H.224), got %d", msg.MediasLen())
	}

	// audio: G722 preferred, should be accepted
	audio := msg.Media(0)
	if audio.GetMedia() != "audio" {
		t.Errorf("media 0: expected 'audio', got '%s'", audio.GetMedia())
	}
	if audio.GetPort() == 0 {
		t.Error("media 0 (audio): expected port > 0")
	}

	// video main (content:main)
	videoMain := msg.Media(1)
	if videoMain.GetMedia() != "video" {
		t.Errorf("media 1: expected 'video', got '%s'", videoMain.GetMedia())
	}
	if videoMain.GetPort() == 0 {
		t.Error("media 1 (video main): expected port > 0")
	}

	// video slides (content:slides)
	videoSlides := msg.Media(2)
	if videoSlides.GetMedia() != "video" {
		t.Errorf("media 2: expected 'video', got '%s'", videoSlides.GetMedia())
	}
	if videoSlides.GetPort() == 0 {
		t.Error("media 2 (video slides): expected port > 0")
	}

	// BFCP: accepted
	bfcpMedia := msg.Media(3)
	if bfcpMedia.GetPort() == 0 {
		t.Error("media 3 (BFCP): expected port > 0")
	}
	if bfcpMedia.GetProto() != "UDP/BFCP" {
		t.Errorf("media 3 (BFCP): expected proto 'UDP/BFCP', got '%s'", bfcpMedia.GetProto())
	}
	if v := bfcpMedia.GetAttributeVal("bfcpver"); v != "1" {
		t.Errorf("media 3 (BFCP): expected bfcpver '1', got '%s'", v)
	}

	// H.224 FECC: disabled (unsupported application type)
	if msg.Media(4).GetPort() != 0 {
		t.Errorf("media 4 (H.224): expected port 0, got %d", msg.Media(4).GetPort())
	}

	f.close()
}

// Poly Studio X / G7500: audio + video(main) + video(slides) + BFCP
// Audio offers G7221 first (unsupported), should fall back to G722/PCMU/PCMA
func TestNegotiate_PolyStudioX(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuAnyCaps(), h264AnyCaps()})

	answer := f.emitOffer(t, polyStudioXOffer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in answer (audio, video main, video slides, BFCP), got %d", msg.MediasLen())
	}

	// audio: G7221 not supported, should match PCMU
	audio := msg.Media(0)
	if audio.GetMedia() != "audio" {
		t.Errorf("media 0: expected 'audio', got '%s'", audio.GetMedia())
	}
	if audio.GetPort() == 0 {
		t.Error("media 0 (audio): expected port > 0")
	}

	// video main
	if msg.Media(1).GetMedia() != "video" {
		t.Errorf("media 1: expected 'video', got '%s'", msg.Media(1).GetMedia())
	}
	if msg.Media(1).GetPort() == 0 {
		t.Error("media 1 (video main): expected port > 0")
	}

	// video slides
	if msg.Media(2).GetMedia() != "video" {
		t.Errorf("media 2: expected 'video', got '%s'", msg.Media(2).GetMedia())
	}
	if msg.Media(2).GetPort() == 0 {
		t.Error("media 2 (video slides): expected port > 0")
	}

	// BFCP: accepted
	bfcpMedia := msg.Media(3)
	if bfcpMedia.GetPort() == 0 {
		t.Error("media 3 (BFCP): expected port > 0")
	}
	if bfcpMedia.GetProto() != "UDP/BFCP" {
		t.Errorf("media 3 (BFCP): expected proto 'UDP/BFCP', got '%s'", bfcpMedia.GetProto())
	}
	if v := bfcpMedia.GetAttributeVal("bfcpver"); v != "1" {
		t.Errorf("media 3 (BFCP): expected bfcpver '1', got '%s'", v)
	}

	f.close()
}

// Poly HDX Legacy: audio + video(main only) + BFCP
// Simpler offer, no slides stream in initial INVITE
func TestNegotiate_PolyLegacy(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{g722AnyCaps(), h264AnyCaps()})

	answer := f.emitOffer(t, polyLegacyOffer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 3 {
		t.Fatalf("expected 3 medias in answer (audio, video, BFCP), got %d", msg.MediasLen())
	}

	// audio
	if msg.Media(0).GetMedia() != "audio" {
		t.Errorf("media 0: expected 'audio', got '%s'", msg.Media(0).GetMedia())
	}
	if msg.Media(0).GetPort() == 0 {
		t.Error("media 0 (audio): expected port > 0")
	}

	// video main
	if msg.Media(1).GetMedia() != "video" {
		t.Errorf("media 1: expected 'video', got '%s'", msg.Media(1).GetMedia())
	}
	if msg.Media(1).GetPort() == 0 {
		t.Error("media 1 (video main): expected port > 0")
	}

	// BFCP: accepted
	bfcpMedia := msg.Media(2)
	if bfcpMedia.GetPort() == 0 {
		t.Error("media 2 (BFCP): expected port > 0")
	}
	if bfcpMedia.GetProto() != "UDP/BFCP" {
		t.Errorf("media 2 (BFCP): expected proto 'UDP/BFCP', got '%s'", bfcpMedia.GetProto())
	}
	if v := bfcpMedia.GetAttributeVal("bfcpver"); v != "1" {
		t.Errorf("media 2 (BFCP): expected bfcpver '1', got '%s'", v)
	}

	f.close()
}

// Yealink VC800: audio + video(main) + video(slides) + BFCP
// Cleanest SDP among vendors, sequential ports
func TestNegotiate_YealinkVC800(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{g722AnyCaps(), pcmuAnyCaps(), h264AnyCaps()})

	answer := f.emitOffer(t, yealinkVC800Offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in answer (audio, video main, video slides, BFCP), got %d", msg.MediasLen())
	}

	// audio
	audio := msg.Media(0)
	if audio.GetMedia() != "audio" {
		t.Errorf("media 0: expected 'audio', got '%s'", audio.GetMedia())
	}
	if audio.GetPort() == 0 {
		t.Error("media 0 (audio): expected port > 0")
	}

	// video main
	if msg.Media(1).GetMedia() != "video" {
		t.Errorf("media 1: expected 'video', got '%s'", msg.Media(1).GetMedia())
	}
	if msg.Media(1).GetPort() == 0 {
		t.Error("media 1 (video main): expected port > 0")
	}

	// video slides
	if msg.Media(2).GetMedia() != "video" {
		t.Errorf("media 2: expected 'video', got '%s'", msg.Media(2).GetMedia())
	}
	if msg.Media(2).GetPort() == 0 {
		t.Error("media 2 (video slides): expected port > 0")
	}

	// BFCP: accepted
	bfcpMedia := msg.Media(3)
	if bfcpMedia.GetPort() == 0 {
		t.Error("media 3 (BFCP): expected port > 0")
	}
	if bfcpMedia.GetProto() != "UDP/BFCP" {
		t.Errorf("media 3 (BFCP): expected proto 'UDP/BFCP', got '%s'", bfcpMedia.GetProto())
	}
	if v := bfcpMedia.GetAttributeVal("bfcpver"); v != "1" {
		t.Errorf("media 3 (BFCP): expected bfcpver '1', got '%s'", v)
	}

	f.close()
}

// Cisco Room Kit with audio-only formats configured.
// Should accept audio, disable all video/BFCP/FECC.
func TestNegotiate_CiscoRoomKit_AudioOnly(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuAnyCaps()})

	answer := f.emitOffer(t, ciscoRoomKitOffer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 5 {
		t.Fatalf("expected 5 medias in answer, got %d", msg.MediasLen())
	}

	// audio accepted
	if msg.Media(0).GetPort() == 0 {
		t.Error("media 0 (audio): expected port > 0")
	}
	// video main: disabled (no video format configured)
	if msg.Media(1).GetPort() != 0 {
		t.Errorf("media 1 (video main): expected port 0, got %d", msg.Media(1).GetPort())
	}
	// video slides: disabled
	if msg.Media(2).GetPort() != 0 {
		t.Errorf("media 2 (video slides): expected port 0, got %d", msg.Media(2).GetPort())
	}
	// BFCP: accepted
	if msg.Media(3).GetPort() == 0 {
		t.Error("media 3 (BFCP): expected port > 0")
	}
	// H.224: disabled
	if msg.Media(4).GetPort() != 0 {
		t.Errorf("media 4 (H.224): expected port 0, got %d", msg.Media(4).GetPort())
	}

	f.close()
}

// Re-INVITE: identical SDP sent twice. Existing tracks should be reused
// with the same local ports.
func TestReInvite_IdenticalSDP(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuAnyCaps(), h264AnyCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
	)

	// First offer: creates tracks
	answer1 := f.emitOffer(t, offer)
	if answer1 == "" {
		t.Fatal("first offer: expected non-empty answer")
	}
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_offer_1")
	msg1 := parseAnswer(t, answer1)
	if msg1.MediasLen() != 2 {
		t.Fatalf("first offer: expected 2 medias, got %d", msg1.MediasLen())
	}
	audioPort1 := msg1.Media(0).GetPort()
	videoPort1 := msg1.Media(1).GetPort()
	if audioPort1 == 0 {
		t.Fatal("first offer: audio port should be > 0")
	}
	if videoPort1 == 0 {
		t.Fatal("first offer: video port should be > 0")
	}

	// Second offer (identical): should reuse existing tracks, same ports
	answer2 := f.emitOffer(t, offer)
	if answer2 == "" {
		t.Fatal("second offer: expected non-empty answer")
	}
	f.emitAck(t)
	msg2 := parseAnswer(t, answer2)
	if msg2.MediasLen() != 2 {
		t.Fatalf("second offer: expected 2 medias, got %d", msg2.MediasLen())
	}
	audioPort2 := msg2.Media(0).GetPort()
	videoPort2 := msg2.Media(1).GetPort()

	if audioPort2 != audioPort1 {
		t.Errorf("re-INVITE: audio port changed from %d to %d, expected reuse", audioPort1, audioPort2)
	}
	if videoPort2 != videoPort1 {
		t.Errorf("re-INVITE: video port changed from %d to %d, expected reuse", videoPort1, videoPort2)
	}
	dumpDot(t, f.pipeline, "after_offer_2")

	f.close()
}

// Re-INVITE: Poly legacy pattern. Initial offer has audio + video(main) + BFCP.
// Re-INVITE adds video(slides) appended after BFCP (RFC 3264: existing mlines
// keep their indices, new media is appended).
var polyLegacyReInviteOffer = strings.Join([]string{
	"v=0",
	"o=Opal 9876 2 IN IP4 10.0.2.30",
	"s=SIP Call",
	"c=IN IP4 10.0.2.30",
	"b=AS:2048",
	"t=0 0",
	"m=audio 20000 RTP/AVP 115 9 0 8 101",
	"a=rtpmap:115 G7221/16000",
	"a=fmtp:115 bitrate=32000",
	"a=rtpmap:9 G722/8000",
	"a=rtpmap:0 PCMU/8000",
	"a=rtpmap:8 PCMA/8000",
	"a=rtpmap:101 telephone-event/8000",
	"a=fmtp:101 0-15",
	"a=sendrecv",
	"m=video 22300 RTP/AVP 97",
	"b=TIAS:1500000",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=3600;max-mbps=108000;level-asymmetry-allowed=1",
	"a=content:main",
	"a=label:11",
	"a=sendrecv",
	"a=rtcp-fb:* nack pli",
	"a=rtcp-fb:* ccm fir",
	"m=application 29299 UDP/BFCP *",
	"a=floorctrl:c-s",
	"a=confid:1",
	"a=userid:8",
	"a=floorid:2 mstrm:12",
	"a=bfcpver:1",
	"m=video 22400 RTP/AVP 97",
	"b=TIAS:1500000",
	"a=rtpmap:97 H264/90000",
	"a=fmtp:97 profile-level-id=42E01F;packetization-mode=1;max-fs=8160;max-mbps=244800;level-asymmetry-allowed=1",
	"a=content:slides",
	"a=label:12",
	"a=sendrecv",
	"",
}, "\r\n")

func TestReInvite_PolyLegacy_AddSlidesStream(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{g722AnyCaps(), h264AnyCaps()})

	// First offer: audio(0) + video-main(1) + BFCP(2)
	answer1 := f.emitOffer(t, polyLegacyOffer)
	if answer1 == "" {
		t.Fatal("first offer: expected non-empty answer")
	}
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_offer_1")
	msg1 := parseAnswer(t, answer1)
	if msg1.MediasLen() != 3 {
		t.Fatalf("first offer: expected 3 medias, got %d", msg1.MediasLen())
	}
	audioPort1 := msg1.Media(0).GetPort()
	videoPort1 := msg1.Media(1).GetPort()
	if audioPort1 == 0 {
		t.Fatal("first offer: audio port should be > 0")
	}
	if videoPort1 == 0 {
		t.Fatal("first offer: video main port should be > 0")
	}
	bfcpPort1 := msg1.Media(2).GetPort()
	if bfcpPort1 == 0 {
		t.Error("first offer: BFCP port should be > 0")
	}

	// Re-INVITE: audio(0) + video-main(1) + BFCP(2) + video-slides(3)
	answer2 := f.emitOffer(t, polyLegacyReInviteOffer)
	if answer2 == "" {
		t.Fatal("re-INVITE: expected non-empty answer")
	}
	f.emitAck(t)
	msg2 := parseAnswer(t, answer2)
	if msg2.MediasLen() != 4 {
		t.Fatalf("re-INVITE: expected 4 medias, got %d", msg2.MediasLen())
	}

	// Audio should be reused (same port)
	audioPort2 := msg2.Media(0).GetPort()
	if audioPort2 != audioPort1 {
		t.Errorf("re-INVITE: audio port changed from %d to %d, expected reuse", audioPort1, audioPort2)
	}

	// Video main should be reused (same port)
	videoPort2 := msg2.Media(1).GetPort()
	if videoPort2 != videoPort1 {
		t.Errorf("re-INVITE: video main port changed from %d to %d, expected reuse", videoPort1, videoPort2)
	}

	// BFCP should be reused (same port)
	bfcpPort2 := msg2.Media(2).GetPort()
	if bfcpPort2 != bfcpPort1 {
		t.Errorf("re-INVITE: BFCP port changed from %d to %d, expected reuse", bfcpPort1, bfcpPort2)
	}

	// Video slides: new track, should have port > 0
	slidesPort := msg2.Media(3).GetPort()
	if slidesPort == 0 {
		t.Error("re-INVITE: video slides should have port > 0")
	}
	if slidesPort == videoPort1 {
		t.Error("re-INVITE: video slides port should differ from video main port")
	}
	dumpDot(t, f.pipeline, "after_offer_2")

	f.close()
}

// Re-INVITE: Cisco full offer sent twice.
// All 5 media lines should produce identical answers both times.
func TestReInvite_CiscoRoomKit_Identical(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{g722AnyCaps(), pcmuAnyCaps(), h264AnyCaps()})

	answer1 := f.emitOffer(t, ciscoRoomKitOffer)
	if answer1 == "" {
		t.Fatal("first offer: expected non-empty answer")
	}
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_offer_1")
	msg1 := parseAnswer(t, answer1)
	if msg1.MediasLen() != 5 {
		t.Fatalf("first offer: expected 5 medias, got %d", msg1.MediasLen())
	}

	// Capture ports from first answer
	ports1 := make([]uint, msg1.MediasLen())
	for i := range ports1 {
		ports1[i] = msg1.Media(i).GetPort()
	}

	// Second identical offer
	answer2 := f.emitOffer(t, ciscoRoomKitOffer)
	if answer2 == "" {
		t.Fatal("second offer: expected non-empty answer")
	}
	f.emitAck(t)
	msg2 := parseAnswer(t, answer2)
	if msg2.MediasLen() != 5 {
		t.Fatalf("second offer: expected 5 medias, got %d", msg2.MediasLen())
	}

	// Verify all ports match
	for i := range ports1 {
		port2 := msg2.Media(i).GetPort()
		if port2 != ports1[i] {
			t.Errorf("re-INVITE: media %d port changed from %d to %d", i, ports1[i], port2)
		}
	}
	dumpDot(t, f.pipeline, "after_offer_2")

	f.close()
}

// Re-INVITE: Yealink initially audio-only, then full offer with video.
// Simulates a scenario where video is added after initial audio-only call.
func TestReInvite_AudioThenAddVideo(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuAnyCaps(), h264AnyCaps()})

	// First offer: audio only
	offer1 := makeSDP("192.168.1.100",
		"m=audio 10000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
	)
	answer1 := f.emitOffer(t, offer1)
	if answer1 == "" {
		t.Fatal("first offer: expected non-empty answer")
	}
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_offer_1")
	msg1 := parseAnswer(t, answer1)
	if msg1.MediasLen() != 1 {
		t.Fatalf("first offer: expected 1 media, got %d", msg1.MediasLen())
	}
	audioPort1 := msg1.Media(0).GetPort()
	if audioPort1 == 0 {
		t.Fatal("first offer: audio port should be > 0")
	}

	// Re-INVITE: audio + video
	offer2 := makeSDP("192.168.1.100",
		"m=audio 10000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 10002 RTP/AVP 97\r\na=rtpmap:97 H264/90000\r\na=content:main",
	)
	answer2 := f.emitOffer(t, offer2)
	if answer2 == "" {
		t.Fatal("re-INVITE: expected non-empty answer")
	}
	f.emitAck(t)
	msg2 := parseAnswer(t, answer2)
	if msg2.MediasLen() != 2 {
		t.Fatalf("re-INVITE: expected 2 medias, got %d", msg2.MediasLen())
	}

	// Audio should be reused
	audioPort2 := msg2.Media(0).GetPort()
	if audioPort2 != audioPort1 {
		t.Errorf("re-INVITE: audio port changed from %d to %d, expected reuse", audioPort1, audioPort2)
	}

	// Video should be newly created
	videoPort := msg2.Media(1).GetPort()
	if videoPort == 0 {
		t.Error("re-INVITE: video port should be > 0")
	}
	dumpDot(t, f.pipeline, "after_offer_2")

	f.close()
}

// BFCP-specific tests

func TestNegotiate_BFCP_Accepted(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	if msg.MediasLen() != 2 {
		t.Fatalf("expected 2 medias in answer, got %d", msg.MediasLen())
	}

	if msg.Media(0).GetPort() == 0 {
		t.Error("expected audio port > 0")
	}

	bfcp := msg.Media(1)
	if bfcp.GetMedia() != "application" {
		t.Errorf("expected BFCP media type 'application', got '%s'", bfcp.GetMedia())
	}
	if bfcp.GetPort() == 0 {
		t.Error("expected BFCP port > 0, got 0")
	}
	if bfcp.GetProto() != "UDP/BFCP" {
		t.Errorf("expected BFCP proto 'UDP/BFCP', got '%s'", bfcp.GetProto())
	}

	f.close()
}

func TestNegotiate_BFCP_Attributes(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)
	msg := parseAnswer(t, answer)

	bfcp := msg.Media(1)
	if bfcp.GetPort() == 0 {
		t.Fatal("BFCP port is 0 (disabled), cannot test attributes")
	}

	if v := bfcp.GetAttributeVal("floorctrl"); v != "s-only" {
		t.Errorf("expected floorctrl 's-only', got '%s'", v)
	}
	if v := bfcp.GetAttributeVal("bfcpver"); v != "1" {
		t.Errorf("expected bfcpver '1' (matching offer), got '%s'", v)
	}
	if v := bfcp.GetAttributeVal("confid"); v == "" {
		t.Error("expected confid attribute to be present")
	}
	if v := bfcp.GetAttributeVal("userid"); v == "" {
		t.Error("expected userid attribute to be present")
	}
	if v := bfcp.GetAttributeVal("setup"); v != "actpass" {
		t.Errorf("expected setup 'actpass', got '%s'", v)
	}

	f.close()
}

func TestNegotiate_BFCP_VersionNegotiation(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	t.Run("version1", func(t *testing.T) {
		f := newFixture(t, []*gst.Caps{pcmuCaps()})

		offer := makeSDP("192.168.1.1",
			"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
			"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=bfcpver:1",
		)
		answer := f.emitOffer(t, offer)
		f.emitAck(t)
		msg := parseAnswer(t, answer)

		bfcp := msg.Media(1)
		if bfcp.GetPort() == 0 {
			t.Fatal("BFCP port is 0, cannot test version")
		}
		if v := bfcp.GetAttributeVal("bfcpver"); v != "1" {
			t.Errorf("expected bfcpver '1', got '%s'", v)
		}

		f.close()
	})

	t.Run("version_default", func(t *testing.T) {
		f := newFixture(t, []*gst.Caps{pcmuCaps()})

		offer := makeSDP("192.168.1.1",
			"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
			"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s",
		)
		answer := f.emitOffer(t, offer)
		f.emitAck(t)
		msg := parseAnswer(t, answer)

		bfcp := msg.Media(1)
		if bfcp.GetPort() == 0 {
			t.Fatal("BFCP port is 0, cannot test version")
		}
		if v := bfcp.GetAttributeVal("bfcpver"); v != "2" {
			t.Errorf("expected default bfcpver '2', got '%s'", v)
		}

		f.close()
	})
}

func TestNegotiate_BFCP_FloorMapping(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:slides\r\na=label:12",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=floorid:2 mstrm:12\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)
	msg := parseAnswer(t, answer)

	if msg.MediasLen() != 3 {
		t.Fatalf("expected 3 medias, got %d", msg.MediasLen())
	}

	slides := msg.Media(1)
	if slides.GetPort() == 0 {
		t.Fatal("video slides port is 0")
	}

	bfcp := msg.Media(2)
	if bfcp.GetPort() == 0 {
		t.Fatal("BFCP port is 0, cannot test floor mapping")
	}

	floorid := bfcp.GetAttributeVal("floorid")
	if floorid == "" {
		t.Error("expected floorid attribute on BFCP media")
	} else if !strings.Contains(floorid, "mstrm:") {
		t.Errorf("expected floorid to contain 'mstrm:', got '%s'", floorid)
	}

	label := slides.GetAttributeVal("label")
	if label == "" {
		t.Error("expected label attribute on screenshare media")
	}

	f.close()
}

func TestNegotiate_BFCP_UnsupportedProto(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=application 5004 RTP/AVP 125\r\na=rtpmap:125 H224/4800",
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)
	msg := parseAnswer(t, answer)

	if msg.MediasLen() != 2 {
		t.Fatalf("expected 2 medias, got %d", msg.MediasLen())
	}

	if msg.Media(0).GetPort() == 0 {
		t.Error("expected audio port > 0")
	}
	if msg.Media(1).GetPort() != 0 {
		t.Errorf("expected non-BFCP application media disabled, got port %d", msg.Media(1).GetPort())
	}

	f.close()
}

func TestReInvite_BFCP_PortReuse(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)

	answer1 := f.emitOffer(t, offer)
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_offer_1")
	msg1 := parseAnswer(t, answer1)
	bfcpPort1 := msg1.Media(1).GetPort()
	if bfcpPort1 == 0 {
		t.Fatal("first offer: BFCP port should be > 0")
	}

	answer2 := f.emitOffer(t, offer)
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_offer_2")
	msg2 := parseAnswer(t, answer2)
	bfcpPort2 := msg2.Media(1).GetPort()
	if bfcpPort2 != bfcpPort1 {
		t.Errorf("re-INVITE: BFCP port changed from %d to %d, expected reuse", bfcpPort1, bfcpPort2)
	}

	f.close()
}

func TestNegotiate_BFCP_DuplicateAtDifferentIndex(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=bfcpver:1",
		"m=application 5006 UDP/BFCP *\r\na=floorctrl:c-s\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)
	msg := parseAnswer(t, answer)

	if msg.MediasLen() != 3 {
		t.Fatalf("expected 3 medias, got %d", msg.MediasLen())
	}

	if msg.Media(1).GetPort() == 0 {
		t.Error("expected first BFCP port > 0")
	}
	if msg.Media(2).GetPort() != 0 {
		t.Errorf("expected second BFCP disabled, got port %d", msg.Media(2).GetPort())
	}

	f.close()
}

// Helper: emit answer-sdp signal
func (f *sipBinFixture) emitAnswer(t *testing.T, answer string) {
	t.Helper()
	t.Logf("Emitting Answer SDP:\n%s", answer)
	if _, err := f.sipbin.Emit("answer-sdp", answer); err != nil {
		t.Fatalf("failed to emit answer-sdp signal: %v", err)
	}
}

// Helper: connect send-offer-sdp handler that captures the offer
func (f *sipBinFixture) connectSendOffer(t *testing.T) chan string {
	t.Helper()
	ch := make(chan string, 1)
	if _, err := f.sipbin.Connect("send-offer-sdp", func(_ *gst.Element, offer string) {
		t.Logf("Received send-offer-sdp:\n%s", offer)
		ch <- offer
	}); err != nil {
		t.Fatalf("failed to connect send-offer-sdp signal: %v", err)
	}
	return ch
}

// Helper: wait for an offer on the channel, fail if timeout
func waitForOffer(t *testing.T, ch chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case offer := <-ch:
		return offer
	case <-time.After(timeout):
		t.Fatal("timed out waiting for send-offer-sdp signal")
		return ""
	}
}

// Helper: assert no offer was sent within a timeout
func assertNoOffer(t *testing.T, ch chan string, timeout time.Duration) {
	t.Helper()
	select {
	case offer := <-ch:
		t.Fatalf("expected no send-offer-sdp, but got:\n%s", offer)
	case <-time.After(timeout):
		// OK: no offer received
	}
}

// Early re-INVITE tests

func TestEarlyReinvite_Triggered(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	// Offer with audio + video(main) + BFCP → triggers early re-INVITE (has camera + BFCP, no screenshare)
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	// Wait for the early re-INVITE offer
	reInviteOffer := waitForOffer(t, ch, 5500*time.Millisecond)
	msg := parseAnswer(t, reInviteOffer)

	// Should have 4 mlines: audio + video(main) + BFCP + video(screenshare)
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in re-INVITE offer, got %d", msg.MediasLen())
	}
	if msg.Media(0).GetMedia() != "audio" {
		t.Errorf("mline 0: expected 'audio', got '%s'", msg.Media(0).GetMedia())
	}
	if msg.Media(1).GetMedia() != "video" {
		t.Errorf("mline 1: expected 'video', got '%s'", msg.Media(1).GetMedia())
	}
	if msg.Media(2).GetMedia() != "application" {
		t.Errorf("mline 2: expected 'application', got '%s'", msg.Media(2).GetMedia())
	}
	if msg.Media(3).GetMedia() != "video" {
		t.Errorf("mline 3: expected 'video' (screenshare), got '%s'", msg.Media(3).GetMedia())
	}

	f.close()
}

func TestEarlyReinvite_NotTriggered_NoCamera(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})
	ch := f.connectSendOffer(t)

	// Audio + BFCP only, no camera → should NOT trigger
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)
	assertNoOffer(t, ch, 100*time.Millisecond)

	f.close()
}

func TestEarlyReinvite_NotTriggered_NoBfcp(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	// Audio + video(main), no BFCP → should NOT trigger
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)
	assertNoOffer(t, ch, 100*time.Millisecond)

	f.close()
}

func TestEarlyReinvite_NotTriggered_HasScreenshare(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	// Audio + video(main) + video(slides) + BFCP → screenshare already present, should NOT trigger
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 5004 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:slides",
		"m=application 5006 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)
	assertNoOffer(t, ch, 100*time.Millisecond)

	f.close()
}

func TestEarlyReinvite_NotTriggered_NoHandler(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	// Do NOT connect send-offer-sdp handler

	// Audio + video(main) + BFCP → conditions met but no handler
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	// No panic, no crash — just works
	f.close()
}

func TestEarlyReinvite_OfferContent(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)
	answerMsg := parseAnswer(t, answer)

	audioPort := answerMsg.Media(0).GetPort()
	videoPort := answerMsg.Media(1).GetPort()
	bfcpPort := answerMsg.Media(2).GetPort()

	reInviteOffer := waitForOffer(t, ch, 5500*time.Millisecond)
	msg := parseAnswer(t, reInviteOffer)

	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias, got %d", msg.MediasLen())
	}

	// Existing media should preserve ports from initial negotiation
	if msg.Media(0).GetPort() != audioPort {
		t.Errorf("audio port mismatch: offer has %d, initial answer had %d", msg.Media(0).GetPort(), audioPort)
	}
	if msg.Media(1).GetPort() != videoPort {
		t.Errorf("video port mismatch: offer has %d, initial answer had %d", msg.Media(1).GetPort(), videoPort)
	}
	if msg.Media(2).GetPort() != bfcpPort {
		t.Errorf("BFCP port mismatch: offer has %d, initial answer had %d", msg.Media(2).GetPort(), bfcpPort)
	}

	// New screenshare mline should be video with H264
	screenshare := msg.Media(3)
	if screenshare.GetMedia() != "video" {
		t.Errorf("screenshare mline: expected 'video', got '%s'", screenshare.GetMedia())
	}
	// Should have at least one format
	hasFormat := false
	for range screenshare.Formats() {
		hasFormat = true
		break
	}
	if !hasFormat {
		t.Error("screenshare mline: expected at least one format")
	}

	f.close()
}

func TestEarlyReinvite_FullFlow(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	// Step 1: initial offer with audio + camera + BFCP
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 5004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)
	dumpDot(t, f.pipeline, "after_initial_offer")

	// Step 2: wait for re-INVITE offer
	reInviteOffer := waitForOffer(t, ch, 5500*time.Millisecond)
	reInviteMsg := parseAnswer(t, reInviteOffer)
	if reInviteMsg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in re-INVITE offer, got %d", reInviteMsg.MediasLen())
	}

	// Step 3: build answer accepting all media including screenshare
	reInviteAnswer := makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 6002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=application 6004 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
		"m=video 6006 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=content:slides\r\na=label:3",
	)

	// Step 4: emit answer — should process without error
	f.emitAnswer(t, reInviteAnswer)
	dumpDot(t, f.pipeline, "after_reinvite_answer")

	f.close()
}

// Polycom X30 answers our re-INVITE screenshare with vnd.polycom.lpr (pt=116)
// as the first codec, which we never offered. OnAnswerSdp should pick H264
// from the answer and ignore the unsupported codecs.
func TestEarlyReinvite_PolycomX30_UnsupportedCodecInAnswer(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})
	ch := f.connectSendOffer(t)

	// Step 1: Initial offer from Polycom X30
	// audio + video (LPR, H265, H264 variants, H263) + H224 app + BFCP
	offer := makeSDP("192.168.0.35",
		"m=audio 23166 RTP/AVP 115 9 102 0 8 15 18 101\r\n"+
			"a=rtpmap:115 G7221/32000\r\n"+
			"a=fmtp:115 bitrate=48000\r\n"+
			"a=rtpmap:9 G722/8000\r\n"+
			"a=rtpmap:102 G7221/16000\r\n"+
			"a=fmtp:102 bitrate=32000\r\n"+
			"a=rtpmap:0 PCMU/8000\r\n"+
			"a=rtpmap:8 PCMA/8000\r\n"+
			"a=rtpmap:15 G728/8000\r\n"+
			"a=rtpmap:18 G729/8000\r\n"+
			"a=fmtp:18 annexb=no\r\n"+
			"a=rtpmap:101 telephone-event/8000\r\n"+
			"a=fmtp:101 0-15\r\n"+
			"a=sendrecv",
		"m=video 22334 RTP/AVP 116 98 109 110 111 96 34\r\n"+
			"b=TIAS:4096000\r\n"+
			"a=rtpmap:116 vnd.polycom.lpr/9000\r\n"+
			"a=fmtp:116 V=2;minPP=0;PP=150;RS=52;RP=10;PS=1400\r\n"+
			"a=rtpmap:98 H265/90000\r\n"+
			"a=fmtp:98 level-id=90; max-lsr=250675200; max-lps=8355840; max-fps=6000\r\n"+
			"a=rtpmap:109 H264/90000\r\n"+
			"a=fmtp:109 profile-level-id=428020; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtpmap:110 H264/90000\r\n"+
			"a=fmtp:110 profile-level-id=428020; packetization-mode=1; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtpmap:111 H264/90000\r\n"+
			"a=fmtp:111 profile-level-id=640020; packetization-mode=1; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtpmap:96 H263-1998/90000\r\n"+
			"a=fmtp:96 CIF4=2;CIF=1;QCIF=1;SQCIF=1;CUSTOM=432,240,1\r\n"+
			"a=rtpmap:34 H263/90000\r\n"+
			"a=fmtp:34 CIF4=2;CIF=1;QCIF=1;SQCIF=1\r\n"+
			"a=rtcp-fb:* ccm tmmbr\r\n"+
			"a=rtcp-fb:* ccm fir\r\n"+
			"a=sendrecv\r\n"+
			"a=content:main",
		"m=application 21486 RTP/AVP 100\r\n"+
			"a=rtpmap:100 H224/4800\r\n"+
			"a=sendrecv",
		"m=application 23748 UDP/BFCP *\r\n"+
			"a=floorctrl:c-s\r\n"+
			"a=confid:1\r\n"+
			"a=userid:2\r\n"+
			"a=floorid:1 mstrm:3\r\n"+
			"a=setup:actpass\r\n"+
			"a=connection:new",
	)

	answer := f.emitOffer(t, offer)
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	f.emitAck(t)

	// Step 2: wait for the early re-INVITE offer (earlyReinvite adds screenshare)
	reInviteOffer := waitForOffer(t, ch, 5500*time.Millisecond)
	reInviteMsg := parseAnswer(t, reInviteOffer)
	t.Logf("Re-INVITE offer has %d media lines", reInviteMsg.MediasLen())

	// Step 3: Polycom answers with unsupported codecs first in screenshare
	// The Poly puts vnd.polycom.lpr (pt=116) first, H265, then H264 variants
	// This is non-RFC compliant: it includes codecs we never offered
	polyAnswer := makeSDP("192.168.0.35",
		"m=audio 23166 RTP/AVP 0\r\n"+
			"a=rtpmap:0 PCMU/8000\r\n"+
			"a=sendrecv",
		"m=video 22334 RTP/AVP 111\r\n"+
			"b=TIAS:4096000\r\n"+
			"a=rtpmap:111 H264/90000\r\n"+
			"a=fmtp:111 profile-level-id=640020; packetization-mode=1; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtcp-fb:* ccm tmmbr\r\n"+
			"a=rtcp-fb:* ccm fir\r\n"+
			"a=content:main\r\n"+
			"a=sendrecv",
		"m=application 0 RTP/AVP 100",
		"m=application 23748 UDP/BFCP *\r\n"+
			"a=floorctrl:c-only\r\n"+
			"a=floorid:1 m-stream:3\r\n"+
			"a=setup:active\r\n"+
			"a=connection:new",
		"m=video 26416 RTP/AVP 116 98 109 110 111 96 34\r\n"+
			"b=TIAS:4096000\r\n"+
			"a=rtpmap:116 vnd.polycom.lpr/9000\r\n"+
			"a=fmtp:116 V=2;minPP=0;PP=150;RS=52;RP=10;PS=1400\r\n"+
			"a=rtpmap:98 H265/90000\r\n"+
			"a=fmtp:98 level-id=90; max-lsr=250675200; max-lps=8355840; max-fps=6000\r\n"+
			"a=rtpmap:109 H264/90000\r\n"+
			"a=fmtp:109 profile-level-id=428020; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtpmap:110 H264/90000\r\n"+
			"a=fmtp:110 profile-level-id=428020; packetization-mode=1; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtpmap:111 H264/90000\r\n"+
			"a=fmtp:111 profile-level-id=640020; packetization-mode=1; max-mbps=490000; max-fs=8192; sar-supported=13; sar=13\r\n"+
			"a=rtpmap:96 H263-1998/90000\r\n"+
			"a=fmtp:96 CIF4=2;CIF=1;QCIF=1;SQCIF=1;CUSTOM=432,240,1\r\n"+
			"a=rtpmap:34 H263/90000\r\n"+
			"a=fmtp:34 CIF4=2;CIF=1;QCIF=1;SQCIF=1\r\n"+
			"a=rtcp-fb:* ccm tmmbr\r\n"+
			"a=rtcp-fb:* ccm fir\r\n"+
			"a=content:slides\r\n"+
			"a=sendrecv",
	)

	// Step 4: emit Polycom's answer — should not crash or select unsupported codecs
	f.emitAnswer(t, polyAnswer)
	dumpDot(t, f.pipeline, "after_poly_reinvite_answer")

	// Step 5: request the screenshare send pad — its capsfilter should have H264 caps,
	// not vnd.polycom.lpr or any other unsupported codec
	pad := f.sipbin.GetRequestPad("send_rtp_sink_3") // 3 = SCREEN_SHARE
	if pad == nil {
		t.Fatal("failed to request send_rtp_sink_3 pad for screenshare")
	}
	padCaps := pad.GetCurrentCaps()
	if padCaps == nil {
		padCaps = pad.QueryCaps(nil)
	}
	if padCaps == nil || padCaps.IsEmpty() {
		t.Fatal("screenshare pad has no caps")
	}

	h264Filter := gst.NewCapsFromString("application/x-rtp,encoding-name=H264")
	if padCaps.IntersectFull(h264Filter, gst.CapsIntersectFirst).IsEmpty() {
		t.Fatalf("screenshare capsfilter has non-H264 caps: %s", padCaps.String())
	}

	f.close()
}

// --- Request Pad Tests ---

func TestRequestPad_AudioOnly(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)

	// MICROPHONE = 2, should succeed after audio negotiation
	pad := f.sipbin.GetRequestPad("send_rtp_sink_2")
	if pad == nil {
		t.Fatal("expected request pad for negotiated audio (MICROPHONE), got nil")
	}
	dumpDot(t, f.pipeline, "after_request_pad")

	f.close()
}

func TestRequestPad_VideoOnly(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{h264Caps()})

	offer := makeSDP("192.168.1.1",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)

	// CAMERA = 1, should succeed after video negotiation
	pad := f.sipbin.GetRequestPad("send_rtp_sink_1")
	if pad == nil {
		t.Fatal("expected request pad for negotiated video (CAMERA), got nil")
	}
	dumpDot(t, f.pipeline, "after_request_pad")

	f.close()
}

func TestRequestPad_AudioAndVideo(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)

	// Both MICROPHONE (2) and CAMERA (1) should succeed
	audioPad := f.sipbin.GetRequestPad("send_rtp_sink_2")
	if audioPad == nil {
		t.Fatal("expected request pad for negotiated audio (MICROPHONE), got nil")
	}

	videoPad := f.sipbin.GetRequestPad("send_rtp_sink_1")
	if videoPad == nil {
		t.Fatal("expected request pad for negotiated video (CAMERA), got nil")
	}
	dumpDot(t, f.pipeline, "after_request_pads")

	f.close()
}

func TestRequestPad_NonNegotiatedMedia(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	// Only negotiate audio
	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)

	// CAMERA (1) was not negotiated, should return nil
	pad := f.sipbin.GetRequestPad("send_rtp_sink_1")
	if pad != nil {
		t.Fatal("expected nil pad for non-negotiated video (CAMERA)")
	}

	// SCREEN_SHARE (3) was not negotiated, should return nil
	pad = f.sipbin.GetRequestPad("send_rtp_sink_3")
	if pad != nil {
		t.Fatal("expected nil pad for non-negotiated SCREEN_SHARE")
	}

	// SCREEN_SHARE_AUDIO (4) was not negotiated, should return nil
	pad = f.sipbin.GetRequestPad("send_rtp_sink_4")
	if pad != nil {
		t.Fatal("expected nil pad for non-negotiated SCREEN_SHARE_AUDIO")
	}

	f.close()
}

func TestRequestPad_InvalidSession(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)

	// Session 0 (TrackSource_UNKNOWN) is not a valid track source
	pad := f.sipbin.GetRequestPad("send_rtp_sink_0")
	if pad != nil {
		t.Fatal("expected nil pad for invalid session 0")
	}

	// Session 99 is out of range
	pad = f.sipbin.GetRequestPad("send_rtp_sink_99")
	if pad != nil {
		t.Fatal("expected nil pad for out-of-range session 99")
	}

	f.close()
}

func TestRequestPad_BeforeNegotiation(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	// Request pads before any SDP negotiation — all should fail
	pad := f.sipbin.GetRequestPad("send_rtp_sink_1")
	if pad != nil {
		t.Fatal("expected nil pad for CAMERA before negotiation")
	}

	pad = f.sipbin.GetRequestPad("send_rtp_sink_2")
	if pad != nil {
		t.Fatal("expected nil pad for MICROPHONE before negotiation")
	}

	f.close()
}

func TestRequestPad_ScreenShare(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := makeSDP("192.168.1.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 5004 RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides",
	)
	f.emitOffer(t, offer)
	f.emitAck(t)

	// SCREEN_SHARE (3) should succeed — slides content was negotiated
	pad := f.sipbin.GetRequestPad("send_rtp_sink_3")
	if pad == nil {
		t.Fatal("expected request pad for negotiated SCREEN_SHARE, got nil")
	}
	dumpDot(t, f.pipeline, "after_request_pad")

	f.close()
}

// --- Media Flow Helpers ---

func addBufferProbe(t *testing.T, element *gst.Element) *atomic.Int32 {
	t.Helper()
	var count atomic.Int32
	sinkPad := element.GetStaticPad("sink")
	if sinkPad == nil {
		t.Fatal("failed to get sink pad for buffer probe")
	}
	sinkPad.AddProbe(gst.PadProbeTypeBuffer|gst.PadProbeTypeBufferList, func(_ *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return &count
}

// newG711RTPSourceToUDP creates: audiotestsrc(is-live) → capsfilter(8kHz) → mulawenc → rtppcmupay → udpsink
func newG711RTPSourceToUDP(t *testing.T, pipeline *gst.Pipeline, host string, port uint) {
	t.Helper()

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=8000,channels=1,format=S16LE"))

	mulawEnc, err := gst.NewElement("mulawenc")
	if err != nil {
		t.Fatal("failed to create mulawenc:", err)
	}
	rtpPay, err := gst.NewElement("rtppcmupay")
	if err != nil {
		t.Fatal("failed to create rtppcmupay:", err)
	}
	udpSink, err := gst.NewElementWithProperties("udpsink", map[string]any{
		"host": host,
		"port": int(port),
		"sync": false,
	})
	if err != nil {
		t.Fatal("failed to create udpsink:", err)
	}

	elements := []*gst.Element{audioSrc, capsFilter, mulawEnc, rtpPay, udpSink}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add G711 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link G711 RTP source chain:", err)
	}
}

// newH264RTPSourceToUDP creates: videotestsrc(is-live) → videoconvert → capsfilter → x264enc → rtph264pay(pt) → udpsink
func newH264RTPSourceToUDP(t *testing.T, pipeline *gst.Pipeline, host string, port uint, pt int) {
	t.Helper()

	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	x264Enc, err := gst.NewElementWithProperties("x264enc", map[string]any{
		"tune":         0x4, // zerolatency
		"speed-preset": 1,   // ultrafast
		"key-int-max":  30,
		"bframes":      0,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}
	rtpPay, err := gst.NewElementWithProperties("rtph264pay", map[string]any{"pt": uint(pt)})
	if err != nil {
		t.Fatal("failed to create rtph264pay:", err)
	}
	udpSink, err := gst.NewElementWithProperties("udpsink", map[string]any{
		"host": host,
		"port": int(port),
		"sync": false,
	})
	if err != nil {
		t.Fatal("failed to create udpsink:", err)
	}

	elements := []*gst.Element{videoSrc, videoConvert, capsFilter, x264Enc, rtpPay, udpSink}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add H264 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link H264 RTP source chain:", err)
	}
}

// newG711RTPSource creates: audiotestsrc(is-live) → capsfilter(8kHz) → mulawenc → rtppcmupay
// Returns the last element and its src pad for linking.
func newG711RTPSource(t *testing.T, pipeline *gst.Pipeline) (*gst.Element, *gst.Pad) {
	t.Helper()

	audioSrc, err := gst.NewElementWithProperties("audiotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create audiotestsrc:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("audio/x-raw,rate=8000,channels=1,format=S16LE"))

	mulawEnc, err := gst.NewElement("mulawenc")
	if err != nil {
		t.Fatal("failed to create mulawenc:", err)
	}
	rtpPay, err := gst.NewElement("rtppcmupay")
	if err != nil {
		t.Fatal("failed to create rtppcmupay:", err)
	}

	elements := []*gst.Element{audioSrc, capsFilter, mulawEnc, rtpPay}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add G711 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link G711 RTP source chain:", err)
	}

	return rtpPay, rtpPay.GetStaticPad("src")
}

// newH264RTPSource creates: videotestsrc(is-live) → videoconvert → capsfilter → x264enc → rtph264pay(pt)
// Returns the last element and its src pad for linking.
func newH264RTPSource(t *testing.T, pipeline *gst.Pipeline, pt int) (*gst.Element, *gst.Pad) {
	t.Helper()

	videoSrc, err := gst.NewElementWithProperties("videotestsrc", map[string]any{"is-live": true})
	if err != nil {
		t.Fatal("failed to create videotestsrc:", err)
	}
	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		t.Fatal("failed to create videoconvert:", err)
	}
	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		t.Fatal("failed to create capsfilter:", err)
	}
	capsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-raw,format=I420,width=320,height=240,framerate=15/1"))

	x264Enc, err := gst.NewElementWithProperties("x264enc", map[string]any{
		"tune":         0x4, // zerolatency
		"speed-preset": 1,   // ultrafast
		"key-int-max":  30,
		"bframes":      0,
	})
	if err != nil {
		t.Fatal("failed to create x264enc:", err)
	}
	rtpPay, err := gst.NewElementWithProperties("rtph264pay", map[string]any{"pt": uint(pt)})
	if err != nil {
		t.Fatal("failed to create rtph264pay:", err)
	}

	elements := []*gst.Element{videoSrc, videoConvert, capsFilter, x264Enc, rtpPay}
	if err := pipeline.AddMany(elements...); err != nil {
		t.Fatal("failed to add H264 RTP source elements:", err)
	}
	if err := gst.ElementLinkMany(elements...); err != nil {
		t.Fatal("failed to link H264 RTP source chain:", err)
	}

	return rtpPay, rtpPay.GetStaticPad("src")
}

// newUDPReceiver creates a udpsrc → fakesink chain listening on a given port, returns the fakesink for probing.
func newUDPReceiver(t *testing.T, pipeline *gst.Pipeline, port int) *gst.Element {
	t.Helper()

	udpSrc, err := gst.NewElementWithProperties("udpsrc", map[string]any{
		"port": port,
	})
	if err != nil {
		t.Fatal("failed to create udpsrc:", err)
	}
	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}

	if err := pipeline.AddMany(udpSrc, sink); err != nil {
		t.Fatal("failed to add UDP receiver elements:", err)
	}
	if err := gst.ElementLinkMany(udpSrc, sink); err != nil {
		t.Fatal("failed to link UDP receiver chain:", err)
	}

	return sink
}

// --- Media Flow Tests ---

func TestMediaFlow_AudioReceive(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps()})

	offer := makeSDP("127.0.0.1", "m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000")
	answer := f.emitOffer(t, offer)
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	audioPort := msg.Media(0).GetPort()
	t.Logf("sipbin listening for audio on port %d", audioPort)

	// Fakesink for receive-path output
	sink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create fakesink:", err)
	}
	if err := f.pipeline.Add(sink); err != nil {
		t.Fatal("failed to add fakesink:", err)
	}
	bufferCount := addBufferProbe(t, sink)

	// Connect pad-added to link recv_rtp_src to fakesink (use weak ref to avoid ref cycle)
	audioPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_MICROPHONE)
	wsink := glib.WeakRefInit(sink)
	f.sipbin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		if strings.HasPrefix(pad.GetName(), audioPrefix) {
			s := gst.ToElement(wsink.Get())
			if s == nil {
				return
			}
			if !s.SyncStateWithParent() {
				t.Logf("warning: failed to sync fakesink state")
			}
			if ret := pad.Link(s.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
			}
		}
	})

	// RTP source sends to sipbin's listening port
	newG711RTPSourceToUDP(t, f.pipeline, "127.0.0.1", audioPort)

	if err := f.pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(4 * time.Second)

	dumpDot(t, f.pipeline, "audio_receive")

	if err := f.pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	count := bufferCount.Load()
	t.Logf("received %d audio buffers", count)
	if count <= 0 {
		t.Fatal("no audio buffers received")
	}

	f.pipeline = nil
	f.sipbin = nil
}

func TestMediaFlow_AudioAndVideoReceive(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := makeSDP("127.0.0.1",
		"m=audio 5000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 5002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	audioPort := msg.Media(0).GetPort()
	videoPort := msg.Media(1).GetPort()
	t.Logf("sipbin listening: audio=%d video=%d", audioPort, videoPort)

	// Fakesinks for receive-path output
	audioSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create audio fakesink:", err)
	}
	videoSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create video fakesink:", err)
	}
	if err := f.pipeline.AddMany(audioSink, videoSink); err != nil {
		t.Fatal("failed to add fakesinks:", err)
	}

	audioCount := addBufferProbe(t, audioSink)
	videoCount := addBufferProbe(t, videoSink)

	audioPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_MICROPHONE)
	videoPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_CAMERA)

	// Use weak refs in closure to avoid ref cycles
	waudioSink := glib.WeakRefInit(audioSink)
	wvideoSink := glib.WeakRefInit(videoSink)

	f.sipbin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		name := pad.GetName()
		switch {
		case strings.HasPrefix(name, audioPrefix):
			s := gst.ToElement(waudioSink.Get())
			if s == nil {
				return
			}
			if !s.SyncStateWithParent() {
				t.Logf("warning: failed to sync audio fakesink state")
			}
			if ret := pad.Link(s.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link audio src pad: %v", ret)
			}
		case strings.HasPrefix(name, videoPrefix):
			s := gst.ToElement(wvideoSink.Get())
			if s == nil {
				return
			}
			if !s.SyncStateWithParent() {
				t.Logf("warning: failed to sync video fakesink state")
			}
			if ret := pad.Link(s.GetStaticPad("sink")); ret != gst.PadLinkOK {
				t.Logf("warning: failed to link video src pad: %v", ret)
			}
		}
	})

	// RTP sources send to sipbin's listening ports
	newG711RTPSourceToUDP(t, f.pipeline, "127.0.0.1", audioPort)
	newH264RTPSourceToUDP(t, f.pipeline, "127.0.0.1", videoPort, 120)

	if err := f.pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(4 * time.Second)

	dumpDot(t, f.pipeline, "audio_and_video_receive")

	if err := f.pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	ac := audioCount.Load()
	vc := videoCount.Load()
	t.Logf("received %d audio buffers, %d video buffers", ac, vc)
	if ac <= 0 {
		t.Fatal("no audio buffers received")
	}
	if vc <= 0 {
		t.Fatal("no video buffers received")
	}

	f.pipeline = nil
	f.sipbin = nil
}

// allocUDPPort binds a UDP socket on localhost to get a free port, then closes it.
func allocUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("failed to allocate UDP port:", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}

func TestMediaFlow_FullDuplex(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	// Allocate ports for the "remote" side (where sipbin sends TO)
	audioRecvPort := allocUDPPort(t)
	cameraRecvPort := allocUDPPort(t)
	screenshareRecvPort := allocUDPPort(t)

	offer := makeSDP("127.0.0.1",
		fmt.Sprintf("m=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000", audioRecvPort),
		fmt.Sprintf("m=video %d RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main", cameraRecvPort),
		fmt.Sprintf("m=video %d RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides", screenshareRecvPort),
	)
	answer := f.emitOffer(t, offer)
	f.emitAck(t)

	msg := parseAnswer(t, answer)
	sipAudioPort := msg.Media(0).GetPort()
	sipCameraPort := msg.Media(1).GetPort()
	sipScreensharePort := msg.Media(2).GetPort()
	t.Logf("sipbin listening: audio=%d camera=%d screenshare=%d", sipAudioPort, sipCameraPort, sipScreensharePort)

	// --- Receive path: send RTP to sipbin, verify recv_rtp_src pads appear ---

	audioRecvSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create audio recv fakesink:", err)
	}
	cameraRecvSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create camera recv fakesink:", err)
	}
	screenshareRecvSink, err := gst.NewElementWithProperties("fakesink", map[string]any{"sync": false, "async": false})
	if err != nil {
		t.Fatal("failed to create screenshare recv fakesink:", err)
	}
	if err := f.pipeline.AddMany(audioRecvSink, cameraRecvSink, screenshareRecvSink); err != nil {
		t.Fatal("failed to add recv fakesinks:", err)
	}

	audioRecvCount := addBufferProbe(t, audioRecvSink)
	cameraRecvCount := addBufferProbe(t, cameraRecvSink)
	screenshareRecvCount := addBufferProbe(t, screenshareRecvSink)

	micPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_MICROPHONE)
	camPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_CAMERA)
	ssPrefix := fmt.Sprintf("recv_rtp_src_%d_", livekit.TrackSource_SCREEN_SHARE)

	waudioRecvSink := glib.WeakRefInit(audioRecvSink)
	wcameraRecvSink := glib.WeakRefInit(cameraRecvSink)
	wscreenshareRecvSink := glib.WeakRefInit(screenshareRecvSink)

	f.sipbin.Connect("pad-added", func(_ *gst.Element, pad *gst.Pad) {
		name := pad.GetName()
		var ws *glib.WeakRef
		switch {
		case strings.HasPrefix(name, micPrefix):
			ws = waudioRecvSink
		case strings.HasPrefix(name, camPrefix):
			ws = wcameraRecvSink
		case strings.HasPrefix(name, ssPrefix):
			ws = wscreenshareRecvSink
		default:
			return
		}
		s := gst.ToElement(ws.Get())
		if s == nil {
			return
		}
		if !s.SyncStateWithParent() {
			t.Logf("warning: failed to sync recv fakesink state for %s", name)
		}
		if ret := pad.Link(s.GetStaticPad("sink")); ret != gst.PadLinkOK {
			t.Logf("warning: failed to link recv pad %s: %v", name, ret)
		}
	})

	// External RTP sources → sipbin's listening ports
	newG711RTPSourceToUDP(t, f.pipeline, "127.0.0.1", sipAudioPort)
	newH264RTPSourceToUDP(t, f.pipeline, "127.0.0.1", sipCameraPort, 120)
	newH264RTPSourceToUDP(t, f.pipeline, "127.0.0.1", sipScreensharePort, 121)

	// --- Send path: request pads, link RTP sources, verify data exits via UDP ---

	audioSendPad := f.sipbin.GetRequestPad("send_rtp_sink_2")
	if audioSendPad == nil {
		t.Fatal("failed to get send_rtp_sink_2 (MICROPHONE)")
	}
	_, audioSendSrc := newG711RTPSource(t, f.pipeline)
	if ret := audioSendSrc.Link(audioSendPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link audio send source:", ret)
	}

	cameraSendPad := f.sipbin.GetRequestPad("send_rtp_sink_1")
	if cameraSendPad == nil {
		t.Fatal("failed to get send_rtp_sink_1 (CAMERA)")
	}
	_, cameraSendSrc := newH264RTPSource(t, f.pipeline, 120)
	if ret := cameraSendSrc.Link(cameraSendPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link camera send source:", ret)
	}

	screenshareSendPad := f.sipbin.GetRequestPad("send_rtp_sink_3")
	if screenshareSendPad == nil {
		t.Fatal("failed to get send_rtp_sink_3 (SCREEN_SHARE)")
	}
	_, screenshareSendSrc := newH264RTPSource(t, f.pipeline, 121)
	if ret := screenshareSendSrc.Link(screenshareSendPad); ret != gst.PadLinkOK {
		t.Fatal("failed to link screenshare send source:", ret)
	}

	// UDP receivers on the "remote" ports to verify send-path data
	audioSendRecv := newUDPReceiver(t, f.pipeline, audioRecvPort)
	cameraSendRecv := newUDPReceiver(t, f.pipeline, cameraRecvPort)
	screenshareSendRecv := newUDPReceiver(t, f.pipeline, screenshareRecvPort)

	audioSendCount := addBufferProbe(t, audioSendRecv)
	cameraSendCount := addBufferProbe(t, cameraSendRecv)
	screenshareSendCount := addBufferProbe(t, screenshareSendRecv)

	// --- Run ---

	if err := f.pipeline.SetState(gst.StatePlaying); err != nil {
		t.Fatal("failed to set pipeline to PLAYING:", err)
	}

	time.Sleep(4 * time.Second)

	dumpDot(t, f.pipeline, "full_duplex")

	if err := f.pipeline.SetState(gst.StateNull); err != nil {
		t.Fatal("failed to set pipeline to NULL:", err)
	}

	// --- Verify receive path ---
	ar := audioRecvCount.Load()
	cr := cameraRecvCount.Load()
	sr := screenshareRecvCount.Load()
	t.Logf("receive path: audio=%d camera=%d screenshare=%d buffers", ar, cr, sr)
	if ar <= 0 {
		t.Error("no audio buffers received on recv path")
	}
	if cr <= 0 {
		t.Error("no camera buffers received on recv path")
	}
	if sr <= 0 {
		t.Error("no screenshare buffers received on recv path")
	}

	// --- Verify send path ---
	as := audioSendCount.Load()
	cs := cameraSendCount.Load()
	ss := screenshareSendCount.Load()
	t.Logf("send path: audio=%d camera=%d screenshare=%d buffers", as, cs, ss)
	if as <= 0 {
		t.Error("no audio buffers received on send path")
	}
	if cs <= 0 {
		t.Error("no camera buffers received on send path")
	}
	if ss <= 0 {
		t.Error("no screenshare buffers received on send path")
	}

	f.pipeline = nil
	f.sipbin = nil
}

// --- Late Offer Tests ---
// Late offer: INVITE with no SDP body → sipbin generates the offer (200 OK),
// remote sends answer in ACK.

func TestLateOffer_Basic(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	// Empty offer triggers buildOfferSdp
	offer := f.emitOffer(t, "")
	if offer == "" {
		t.Fatal("expected non-empty offer from late offer path")
	}

	msg := parseAnswer(t, offer)

	// Should have audio + video(main) + video(slides) + BFCP
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias in late offer, got %d", msg.MediasLen())
	}

	// Verify media types
	if msg.Media(0).GetMedia() != "audio" {
		t.Errorf("media 0: expected 'audio', got '%s'", msg.Media(0).GetMedia())
	}
	if msg.Media(0).GetPort() == 0 {
		t.Error("media 0 (audio): expected port > 0")
	}
	if msg.Media(1).GetMedia() != "video" {
		t.Errorf("media 1: expected 'video', got '%s'", msg.Media(1).GetMedia())
	}
	if msg.Media(1).GetPort() == 0 {
		t.Error("media 1 (video main): expected port > 0")
	}
	if msg.Media(2).GetMedia() != "video" {
		t.Errorf("media 2: expected 'video', got '%s'", msg.Media(2).GetMedia())
	}
	if msg.Media(2).GetPort() == 0 {
		t.Error("media 2 (video slides): expected port > 0")
	}
	if msg.Media(3).GetMedia() != "application" {
		t.Errorf("media 3: expected 'application', got '%s'", msg.Media(3).GetMedia())
	}
	if msg.Media(3).GetPort() == 0 {
		t.Error("media 3 (BFCP): expected port > 0")
	}

	// Send answer in ACK — accept audio + video main, reject the rest
	answer := makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 6002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 0 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:slides",
		"m=application 0 UDP/BFCP *",
	)
	f.emitAckWithSDP(t, answer)
	dumpDot(t, f.pipeline, "after_late_offer_ack")

	f.close()
}

func TestLateOffer_FullAccept(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := f.emitOffer(t, "")
	if offer == "" {
		t.Fatal("expected non-empty offer")
	}

	msg := parseAnswer(t, offer)
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias, got %d", msg.MediasLen())
	}

	// Accept all media
	answer := makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 6002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 6004 RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides",
		"m=application 6006 UDP/BFCP *\r\na=floorctrl:c-s\r\na=confid:1\r\na=userid:2\r\na=bfcpver:1",
	)
	f.emitAckWithSDP(t, answer)

	// Verify we can request send pads after late offer negotiation
	audioPad := f.sipbin.GetRequestPad("send_rtp_sink_2") // MICROPHONE
	if audioPad == nil {
		t.Error("expected request pad for MICROPHONE after late offer")
	}
	videoPad := f.sipbin.GetRequestPad("send_rtp_sink_1") // CAMERA
	if videoPad == nil {
		t.Error("expected request pad for CAMERA after late offer")
	}
	screensharePad := f.sipbin.GetRequestPad("send_rtp_sink_3") // SCREEN_SHARE
	if screensharePad == nil {
		t.Error("expected request pad for SCREEN_SHARE after late offer")
	}
	dumpDot(t, f.pipeline, "after_late_offer_full_accept")

	f.close()
}

func TestLateOffer_PartialReject(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	offer := f.emitOffer(t, "")
	if offer == "" {
		t.Fatal("expected non-empty offer")
	}

	msg := parseAnswer(t, offer)
	if msg.MediasLen() != 4 {
		t.Fatalf("expected 4 medias, got %d", msg.MediasLen())
	}

	// Accept audio only, reject everything else (port 0)
	answer := makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 0 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 0 RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides",
		"m=application 0 UDP/BFCP *",
	)
	f.emitAckWithSDP(t, answer)

	// Audio pad should work
	audioPad := f.sipbin.GetRequestPad("send_rtp_sink_2")
	if audioPad == nil {
		t.Error("expected request pad for MICROPHONE after late offer with audio accepted")
	}

	// Video pads should not work (rejected)
	videoPad := f.sipbin.GetRequestPad("send_rtp_sink_1")
	if videoPad != nil {
		t.Error("expected nil pad for CAMERA after late offer with video rejected")
	}

	f.close()
}

func TestLateOffer_TransactionState(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuCaps(), h264Caps()})

	// After late offer, transaction should be pending ACK
	offer := f.emitOffer(t, "")
	if offer == "" {
		t.Fatal("expected non-empty offer")
	}

	// Verify transaction is pending (should be pending=Ack)
	val, err := f.sipbin.GetProperty("transaction-pending")
	if err != nil {
		t.Fatalf("failed to get transaction-pending: %v", err)
	}
	pending, ok := val.(int)
	if !ok {
		t.Fatalf("transaction-pending has unexpected type: %T", val)
	}
	if pending != int(TransactionPendingKindAck) {
		t.Errorf("expected transaction-pending=%d (Ack), got %d", TransactionPendingKindAck, pending)
	}

	// Send answer in ACK to complete the transaction
	answer := makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 6002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 6004 RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides",
		"m=application 6006 UDP/BFCP *\r\na=floorctrl:c-s\r\na=bfcpver:1",
	)
	f.emitAckWithSDP(t, answer)

	// Transaction should be idle now
	val, err = f.sipbin.GetProperty("transaction-pending")
	if err != nil {
		t.Fatalf("failed to get transaction-pending: %v", err)
	}
	pending, ok = val.(int)
	if !ok {
		t.Fatalf("transaction-pending has unexpected type: %T", val)
	}
	if pending != int(TransactionPendingKindNone) {
		t.Errorf("expected transaction-pending=%d (None) after ACK, got %d", TransactionPendingKindNone, pending)
	}

	// Should be able to do a normal offer/answer after late offer completes
	reinviteOffer := makeSDP("192.168.1.1",
		"m=audio 7000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 7002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 7004 RTP/AVP 121\r\na=rtpmap:121 H264/90000\r\na=content:slides",
		"m=application 7006 UDP/BFCP *\r\na=floorctrl:c-s\r\na=bfcpver:1",
	)
	reinviteAnswer := f.emitOffer(t, reinviteOffer)
	if reinviteAnswer == "" {
		t.Fatal("expected non-empty answer for re-INVITE after late offer")
	}
	f.emitAck(t)

	f.close()
}

func TestLateOffer_OfferHasFormats(t *testing.T) {
	defer testutils.AssertNoLeaks(t)

	f := newFixture(t, []*gst.Caps{pcmuAnyCaps(), g722AnyCaps(), h264AnyCaps()})

	offer := f.emitOffer(t, "")
	if offer == "" {
		t.Fatal("expected non-empty offer")
	}

	msg := parseAnswer(t, offer)

	// Check that audio media has our configured codecs
	audio := msg.Media(0)
	if audio.GetMedia() != "audio" {
		t.Fatalf("media 0: expected 'audio', got '%s'", audio.GetMedia())
	}

	hasPCMU := false
	hasG722 := false
	for _, format := range audio.Formats() {
		pt, err := strconv.Atoi(format)
		if err != nil {
			continue
		}
		caps, err := audio.GetCaps(pt)
		if err != nil || caps.GetSize() == 0 {
			continue
		}
		encoding, err := caps.GetStructureAt(0).GetString("encoding-name")
		if err != nil {
			continue
		}
		switch encoding {
		case "PCMU":
			hasPCMU = true
		case "G722":
			hasG722 = true
		}
	}
	if !hasPCMU {
		t.Error("expected PCMU in late offer audio formats")
	}
	if !hasG722 {
		t.Error("expected G722 in late offer audio formats")
	}

	// Check that video media has H264
	video := msg.Media(1)
	if video.GetMedia() != "video" {
		t.Fatalf("media 1: expected 'video', got '%s'", video.GetMedia())
	}

	hasH264 := false
	for _, format := range video.Formats() {
		pt, err := strconv.Atoi(format)
		if err != nil {
			continue
		}
		caps, err := video.GetCaps(pt)
		if err != nil || caps.GetSize() == 0 {
			continue
		}
		encoding, err := caps.GetStructureAt(0).GetString("encoding-name")
		if err != nil {
			continue
		}
		if encoding == "H264" {
			hasH264 = true
		}
	}
	if !hasH264 {
		t.Error("expected H264 in late offer video formats")
	}

	// Send answer to complete
	answer := makeSDP("192.168.1.1",
		"m=audio 6000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000",
		"m=video 6002 RTP/AVP 120\r\na=rtpmap:120 H264/90000\r\na=content:main",
		"m=video 0 RTP/AVP 120",
		"m=application 0 UDP/BFCP *",
	)
	f.emitAckWithSDP(t, answer)

	f.close()
}
