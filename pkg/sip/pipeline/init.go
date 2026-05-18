package pipeline

import (
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/bfcpserver"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/h264rtppaybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/hop"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iolivekit"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/iosip"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitbin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/rtpcapscodecfilter"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipbin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/sipcompositor"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/trackfallback"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiog722"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audioopus"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiopcma"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/audiopcmu"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/av1video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/dtmfaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/factorybin"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/flacsource"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/g722audio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/h264video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/opusaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/pcmaaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/pcmuaudio"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoav1"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videoh264"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp8"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/videovp9"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp8video"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/transcode/vp9video"
)

const QDataPadPeerKey = "livekitsip-pad-peer"

var MainLoop *glib.MainLoop

func init() {
	gst.Init(nil)

	if !hop.Register() {
		panic("Failed to register hop src/sink")
	}

	if !sipbin.Register() {
		panic("Failed to register sipbin")
	}

	if !livekitbin.Register() {
		panic("Failed to register livekitbin")
	}

	if !trackfallback.Register() {
		panic("Failed to register trackfallback")
	}

	if !iolivekit.Register() {
		panic("Failed to register io_manager_livekit")
	}

	if !iosip.Register() {
		panic("Failed to register io_manager_sip")
	}

	if !audioopus.Register() {
		panic("Failed to register audio-opus")
	}

	if !opusaudio.Register() {
		panic("Failed to register opus-audio")
	}

	if !h264video.Register() {
		panic("Failed to register h264-video")
	}

	if !videovp8.Register() {
		panic("Failed to register video-vp8")
	}

	if !vp8video.Register() {
		panic("Failed to register vp8-video")
	}

	if !videoh264.Register() {
		panic("Failed to register video-h264")
	}

	if !livekitcompositor.Register() {
		panic("Failed to register livekitcompositor")
	}

	if !sipcompositor.Register() {
		panic("Failed to register sipcompositor")
	}

	if !dtmfaudio.Register() {
		panic("Failed to register dtmf-audio")
	}

	if !flacsource.Register() {
		panic("Failed to register flacsource")
	}

	if !rtpcapscodecfilter.Register() {
		panic("Failed to register rtpcapscodecfilter")
	}

	if !h264rtppaybin.Register() {
		panic("Failed to register h264rtppaybin")
	}

	if !bfcpserver.Register() {
		panic("Failed to register bfcpserver")
	}

	if !audiopcmu.Register() {
		panic("Failed to register audio-pcmu")
	}

	if !pcmuaudio.Register() {
		panic("Failed to register pcmu-audio")
	}

	if !audiopcma.Register() {
		panic("Failed to register audio-pcma")
	}

	if !pcmaaudio.Register() {
		panic("Failed to register pcma-audio")
	}

	if !audiog722.Register() {
		panic("Failed to register audio-g722")
	}

	if !g722audio.Register() {
		panic("Failed to register g722-audio")
	}

	if !vp9video.Register() {
		panic("Failed to register vp9-video")
	}

	if !videovp9.Register() {
		panic("Failed to register video-vp9")
	}

	if !av1video.Register() {
		panic("Failed to register av1-video")
	}

	if !videoav1.Register() {
		panic("Failed to register video-av1")
	}

	if !factorybin.Register() {
		panic("Failed to register factorybin")
	}

	MainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)

	go MainLoop.Run()
}
