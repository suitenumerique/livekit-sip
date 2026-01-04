package camera_pipeline

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtcp"
)

func NewRtcpSsrcFilter(targetSSRC uint32) func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	return func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		if buffer == nil {
			return gst.PadProbeOK
		}

		mapInfoRead := buffer.Map(gst.MapRead)
		inputBytes := make([]byte, mapInfoRead.Size())
		copy(inputBytes, mapInfoRead.Bytes())
		buffer.Unmap()

		packets, err := rtcp.Unmarshal(inputBytes)
		if err != nil {
			return gst.PadProbeOK
		}

		var keptPackets []rtcp.Packet
		for _, pkt := range packets {
			senderSSRC, hasSSRC := getSenderSSRC(pkt)
			if hasSSRC {
				if senderSSRC == targetSSRC {
					keptPackets = append(keptPackets, pkt)
				}
			} else {
				keptPackets = append(keptPackets, pkt)
			}
		}

		if len(keptPackets) == 0 {
			return gst.PadProbeDrop
		}

		newBytes, err := rtcp.Marshal(keptPackets)
		if err != nil {
			return gst.PadProbeDrop
		}

		mapInfoWrite := buffer.Map(gst.MapWrite)
		defer buffer.Unmap()

		// Sould be impossible since we are only removing packets but check anyway
		if len(newBytes) > int(buffer.GetSize()) {
			return gst.PadProbeDrop
		}

		copy(mapInfoWrite.Bytes(), newBytes)

		buffer.Resize(0, int64(len(newBytes)))

		return gst.PadProbeOK
	}
}

func getSenderSSRC(pkt rtcp.Packet) (uint32, bool) {
	switch p := pkt.(type) {
	case *rtcp.SenderReport:
		return p.SSRC, true
	case *rtcp.ReceiverReport:
		return p.SSRC, true
	case *rtcp.Goodbye:
		if len(p.Sources) > 0 {
			return p.Sources[0], true
		}
	case *rtcp.SourceDescription:
		if len(p.Chunks) > 0 {
			return p.Chunks[0].Source, true
		}
	case *rtcp.PictureLossIndication:
		return p.SenderSSRC, true
	case *rtcp.FullIntraRequest:
		return p.SenderSSRC, true
	case *rtcp.ExtendedReport:
		return p.SenderSSRC, true
	}
	return 0, false
}
