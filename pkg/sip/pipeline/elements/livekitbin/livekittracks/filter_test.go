package livekittracks

import (
	"testing"

	"github.com/pion/rtcp"
	"github.com/stretchr/testify/require"
)

func TestFilterSSRC(t *testing.T) {
	const ssrc, other = uint32(0x1111), uint32(0x2222)

	require.Nil(t, filterSSRC(&rtcp.SourceDescription{
		Chunks: []rtcp.SourceDescriptionChunk{{Source: ssrc, Items: []rtcp.SourceDescriptionItem{{Type: rtcp.SDESCNAME, Text: "a"}}}},
	}, ssrc), "a lone SDES is not a valid compound packet for rtpbin")

	sr := &rtcp.SenderReport{SSRC: ssrc}
	require.Same(t, sr, filterSSRC(sr, ssrc))
	require.Nil(t, filterSSRC(&rtcp.SenderReport{SSRC: other}, ssrc))

	rr := &rtcp.ReceiverReport{SSRC: ssrc}
	require.Same(t, rr, filterSSRC(rr, ssrc))

	bye := filterSSRC(&rtcp.Goodbye{Sources: []uint32{other, ssrc}}, ssrc)
	require.Equal(t, []uint32{ssrc}, bye.(*rtcp.Goodbye).Sources)
	require.Nil(t, filterSSRC(&rtcp.Goodbye{Sources: []uint32{other}}, ssrc))

	pli := &rtcp.PictureLossIndication{SenderSSRC: ssrc, MediaSSRC: other}
	require.Same(t, pli, filterSSRC(pli, ssrc))
	require.Nil(t, filterSSRC(&rtcp.PictureLossIndication{SenderSSRC: other}, ssrc))
}
