package sipbin

import (
	"fmt"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
)

type RTPSessionStats struct {
	RtxDropCount    uint32
	SentNackCount   uint32
	RecvNackCount   uint32
	RtxCount        uint32
	RecvRtxReqCount uint32
	SentRtxReqCount uint32
	Sources         []RTPSourceStats
}

type RTPSourceStats struct {
	// Always present
	SSRC        uint32
	Internal    bool
	Validated   bool
	ReceivedBye bool
	IsCSRC      bool
	IsSender    bool
	SeqnumBase  int32 // -1 if unknown
	ClockRate   int32 // -1 if unknown

	// Optional (peer address)
	RTPFrom  string // "" if absent
	RTCPFrom string

	// Always set (counters)
	OctetsSent      uint64
	PacketsSent     uint64
	OctetsReceived  uint64
	PacketsReceived uint64
	BytesReceived   uint64
	Bitrate         uint64 // bits/sec
	PacketsLost     int32
	Jitter          uint32 // in clock-rate units
	SentPLICount    uint32
	RecvPLICount    uint32
	SentFIRCount    uint32
	RecvFIRCount    uint32
	SentNACKCount   uint32
	RecvNACKCount   uint32
	RecvPacketRate  uint32

	// Last SR (have-sr gates the rest)
	HaveSR        bool
	SRNTPTime     uint64 // 32.32 NTP fixed-point
	SRRTPTime     uint32
	SROctetCount  uint32
	SRPacketCount uint32

	// Last RB we sent. Only non-internal sources carry these — SentRB is always
	// false for internal sources, so LastSentRB is nil. RoundTrip is always 0
	// (no sent-rb-round-trip field) and SSRC is implicit (= the parent SSRC).
	SentRB     bool         // sent-rb gate
	LastSentRB *ReportBlock // contents (nil iff !SentRB)

	// Last RB we received about this source.
	// For internal: from remote about us. For non-internal: deprecated mirror
	// of LastSentRB.
	HaveRB bool         // have-rb gate
	LastRB *ReportBlock // contents (nil iff !HaveRB)

	// Internal sources only — most recent RR per peer
	ReceivedRR []ReceiverReport
}

type ReportBlock struct {
	SSRC          uint32 // rb-ssrc
	FractionLost  uint8  // rb-fractionlost (0–255)
	PacketsLost   int32
	ExtHighestSeq uint32
	Jitter        uint32 // clock-rate units
	LSR           uint32 // 16.16 NTP short
	DLSR          uint32 // 16.16 NTP short
	RoundTrip     uint32 // 16.16 NTP short — only on RB, not sent-rb
}

type ReceiverReport struct {
	SSRC          uint32
	SenderSSRC    uint32 // rb-sender-ssrc
	FractionLost  uint8
	PacketsLost   int32
	ExtHighestSeq uint32
	Jitter        uint32
	LSR, DLSR     uint32
	RoundTrip     uint32
}

func (e *SipBin) DumpStats(self *gst.Bin) *gst.Structure {
	st := gst.NewStructure("call-stats")

	for i := range e.Tracks {
		if i == 0 || e.Tracks[i] == nil {
			continue
		}
		kind := livekit.TrackSource(i)
		stats, err := e.getStats(kind)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to get stats for track source %s: %v", kind, err))
			self.Error(fmt.Sprintf("Failed to get stats for track source %s", kind), err)
			continue
		}
		if stats == nil {
			continue
		}

		if err := st.SetValue(kind.String(), glib.ArbitraryValue{Data: stats}); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set stats structure for track source %s: %v", kind, err))
			self.Error(fmt.Sprintf("Failed to set stats structure for track source %s", kind), err)
		}

		if err := st.SetValue(fmt.Sprintf("%s-caps", kind), e.Tracks[kind].Caps.Copy()); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set caps structure for track source %s: %v", kind, err))
			self.Error(fmt.Sprintf("Failed to set caps structure for track source %s", kind), err)
		}
	}

	return st
}

func (e *SipBin) getStats(kind livekit.TrackSource) (*RTPSessionStats, error) {
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
	default:
		return nil, fmt.Errorf("invalid track source: %s(%d)", kind, int(kind))
	}

	track := e.Tracks[kind]
	if track == nil || !track.initialized {
		return nil, nil
	}

	rtpSessionVal, err := e.RtpBin.Emit("get-internal-session", uint(kind))
	if err != nil {
		return nil, fmt.Errorf("failed to get internal session for track source %s: %w", kind, err)
	}
	rtpSession, ok := rtpSessionVal.(*glib.Object)
	if !ok {
		return nil, fmt.Errorf("failed to convert internal session to element for track source %s: %w", kind, err)
	}

	statsVal, err := rtpSession.GetProperty("stats")
	if err != nil {
		return nil, fmt.Errorf("failed to get stats for track source %s: %w", kind, err)
	}
	statsSt, ok := statsVal.(*gst.Structure)
	if !ok {
		return nil, fmt.Errorf("failed to convert stats to RTPStats for track source %s: %w", kind, err)
	}

	stats := toStats(statsSt)
	return &stats, nil
}

func toStats(stats *gst.Structure) RTPSessionStats {
	if stats == nil {
		return RTPSessionStats{}
	}
	out := RTPSessionStats{
		RtxDropCount:    uint32(orDefault(uint(0))(stats.GetUint("rtx-drop-count"))),
		SentNackCount:   uint32(orDefault(uint(0))(stats.GetUint("sent-nack-count"))),
		RecvNackCount:   uint32(orDefault(uint(0))(stats.GetUint("recv-nack-count"))),
		RtxCount:        uint32(orDefault(uint(0))(stats.GetUint("rtx-count"))),
		RecvRtxReqCount: uint32(orDefault(uint(0))(stats.GetUint("recv-rtx-req-count"))),
		SentRtxReqCount: uint32(orDefault(uint(0))(stats.GetUint("sent-rtx-req-count"))),
	}

	v, err := stats.GetValue("source-stats")
	if err != nil {
		return out
	}
	arr, ok := v.(*glib.ValueArray)
	if !ok || arr == nil {
		return out
	}
	out.Sources = make([]RTPSourceStats, 0, arr.Len())
	for i := 0; i < arr.Len(); i++ {
		entry, err := arr.Index(i)
		if err != nil {
			continue
		}
		st, ok := entry.(*gst.Structure)
		if !ok || st == nil {
			continue
		}
		out.Sources = append(out.Sources, toSourceStats(st))
	}

	return out
}

func toSourceStats(s *gst.Structure) RTPSourceStats {
	out := RTPSourceStats{
		SSRC:            uint32(orDefault(uint(0))(s.GetUint("ssrc"))),
		Internal:        orDefault(false)(s.GetBool("internal")),
		Validated:       orDefault(false)(s.GetBool("validated")),
		ReceivedBye:     orDefault(false)(s.GetBool("received-bye")),
		IsCSRC:          orDefault(false)(s.GetBool("is-csrc")),
		IsSender:        orDefault(false)(s.GetBool("is-sender")),
		SeqnumBase:      int32(orDefault(int(0))(s.GetInt("seqnum-base"))),
		ClockRate:       int32(orDefault(int(0))(s.GetInt("clock-rate"))),
		RTPFrom:         orDefault("")(s.GetString("rtp-from")),
		RTCPFrom:        orDefault("")(s.GetString("rtcp-from")),
		OctetsSent:      orDefault(uint64(0))(s.GetUint64("octets-sent")),
		PacketsSent:     orDefault(uint64(0))(s.GetUint64("packets-sent")),
		OctetsReceived:  orDefault(uint64(0))(s.GetUint64("octets-received")),
		PacketsReceived: orDefault(uint64(0))(s.GetUint64("packets-received")),
		BytesReceived:   orDefault(uint64(0))(s.GetUint64("bytes-received")),
		Bitrate:         orDefault(uint64(0))(s.GetUint64("bitrate")),
		PacketsLost:     int32(orDefault(int(0))(s.GetInt("packets-lost"))),
		Jitter:          uint32(orDefault(uint(0))(s.GetUint("jitter"))),
		SentPLICount:    uint32(orDefault(uint(0))(s.GetUint("sent-pli-count"))),
		RecvPLICount:    uint32(orDefault(uint(0))(s.GetUint("recv-pli-count"))),
		SentFIRCount:    uint32(orDefault(uint(0))(s.GetUint("sent-fir-count"))),
		RecvFIRCount:    uint32(orDefault(uint(0))(s.GetUint("recv-fir-count"))),
		SentNACKCount:   uint32(orDefault(uint(0))(s.GetUint("sent-nack-count"))),
		RecvNACKCount:   uint32(orDefault(uint(0))(s.GetUint("recv-nack-count"))),
		RecvPacketRate:  uint32(orDefault(uint(0))(s.GetUint("recv-packet-rate"))),
		HaveSR:          orDefault(false)(s.GetBool("have-sr")),
		SRNTPTime:       orDefault(uint64(0))(s.GetUint64("sr-ntptime")),
		SRRTPTime:       uint32(orDefault(uint(0))(s.GetUint("sr-rtptime"))),
		SROctetCount:    uint32(orDefault(uint(0))(s.GetUint("sr-octet-count"))),
		SRPacketCount:   uint32(orDefault(uint(0))(s.GetUint("sr-packet-count"))),
	}
	out.SentRB = orDefault(false)(s.GetBool("sent-rb"))
	if out.SentRB {
		out.LastSentRB = readReportBlock(s, "sent-rb-")
	}
	out.HaveRB = orDefault(false)(s.GetBool("have-rb"))
	if out.HaveRB {
		out.LastRB = readReportBlock(s, "rb-")
	}
	out.ReceivedRR = readReceivedRR(s)
	return out
}

// readReportBlock extracts the 7- or 8-field report block for the given prefix.
// "sent-rb-" lacks a round-trip field, so RoundTrip will be zero in that case.
func readReportBlock(s *gst.Structure, prefix string) *ReportBlock {
	return &ReportBlock{
		SSRC:          uint32(orDefault(uint(0))(s.GetUint(prefix + "ssrc"))),
		FractionLost:  uint8(orDefault(uint(0))(s.GetUint(prefix + "fractionlost"))),
		PacketsLost:   int32(orDefault(int(0))(s.GetInt(prefix + "packetslost"))),
		ExtHighestSeq: uint32(orDefault(uint(0))(s.GetUint(prefix + "exthighestseq"))),
		Jitter:        uint32(orDefault(uint(0))(s.GetUint(prefix + "jitter"))),
		LSR:           uint32(orDefault(uint(0))(s.GetUint(prefix + "lsr"))),
		DLSR:          uint32(orDefault(uint(0))(s.GetUint(prefix + "dlsr"))),
		RoundTrip:     uint32(orDefault(uint(0))(s.GetUint(prefix + "round-trip"))),
	}
}

func orDefault[T any](defaultValue T) func(value T, err error) T {
	return func(value T, err error) T {
		if err != nil {
			return defaultValue
		}
		return value
	}
}

func readReceivedRR(s *gst.Structure) []ReceiverReport {
	v, err := s.GetValue("received-rr")
	if err != nil {
		return nil
	}
	list, ok := v.(*gst.ValueListValue)
	if !ok || list == nil {
		return nil
	}
	n := list.Size()
	if n == 0 {
		return nil
	}
	out := make([]ReceiverReport, 0, n)
	for i := uint(0); i < n; i++ {
		rr, ok := list.ValueAt(i).(*gst.Structure)
		if !ok || rr == nil {
			continue
		}
		out = append(out, ReceiverReport{
			SSRC:          uint32(orDefault(uint(0))(rr.GetUint("rb-ssrc"))),
			SenderSSRC:    uint32(orDefault(uint(0))(rr.GetUint("rb-sender-ssrc"))),
			FractionLost:  uint8(orDefault(uint(0))(rr.GetUint("rb-fractionlost"))),
			PacketsLost:   int32(orDefault(int(0))(rr.GetInt("rb-packetslost"))),
			ExtHighestSeq: uint32(orDefault(uint(0))(rr.GetUint("rb-exthighestseq"))),
			Jitter:        uint32(orDefault(uint(0))(rr.GetUint("rb-jitter"))),
			LSR:           uint32(orDefault(uint(0))(rr.GetUint("rb-lsr"))),
			DLSR:          uint32(orDefault(uint(0))(rr.GetUint("rb-dlsr"))),
			RoundTrip:     uint32(orDefault(uint(0))(rr.GetUint("rb-round-trip"))),
		})
	}
	return out
}

