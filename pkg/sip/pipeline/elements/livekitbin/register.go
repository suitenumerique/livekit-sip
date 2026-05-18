package livekitbin

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/base"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin/livekittracks"
)

func Register() bool {
	return gst.RegisterElement(
		nil,
		"livekitbin",
		gst.RankNone,
		&LivekitBin{},
		gst.ExtendsBin,
	) && gst.RegisterElement(
		nil,
		"livekitbin_sinktrack",
		gst.RankNone,
		&livekittracks.SinkTrack{},
		base.ExtendsBaseSink,
	) && gst.RegisterElement(
		nil,
		"livekitbin_sinkrtcp",
		gst.RankNone,
		&livekittracks.SinkRtcp{},
		base.ExtendsBaseSink,
	) && gst.RegisterElement(
		nil,
		"livekitbin_srctrack",
		gst.RankNone,
		&livekittracks.SrcTrack{},
		gst.ExtendsBin,
	) && gst.RegisterElement(
		nil,
		"livekitbin_srctrack_rtp",
		gst.RankNone,
		&livekittracks.SrcTrackRtp{},
		base.ExtendsBaseSrc,
	)
}
