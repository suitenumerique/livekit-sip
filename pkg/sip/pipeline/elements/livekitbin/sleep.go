package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor"
	"github.com/samber/lo"
)

func (e *LivekitBin) cameraSleep(self *gst.Bin, p []lksdk.Participant) {
	if !e.config.camera {
		return
	}

	if e.maxActiveParticipants == 0 {
		return
	}

	tileW, tileH := livekitcompositor.CameraTileVideoSize(int(e.videoWidth), int(e.videoHeight), len(p))

	for _, part := range p {
		rp, ok := part.(*lksdk.RemoteParticipant)
		if !ok {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Participant is not a remote participant\nidentity=%s", part.Identity()))
			continue
		}
		camera, ok := rp.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
		if !ok || camera == nil {
			continue
		}

		if !camera.IsSubscribed() {
			if err := camera.SetSubscribed(true); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to camera track\nidentity=%s\nerr=%v", rp.Identity(), err))
			}
		}

		e.cameraRequestDimensions(camera, uint32(tileW), uint32(tileH))

		if !camera.IsEnabled() {
			camera.SetEnabled(true)
		}
	}

	remote := e.room.GetRemoteParticipants()
	inactive := lo.Filter(remote, func(part *lksdk.RemoteParticipant, i int) bool {
		return !lo.ContainsBy(p, func(active lksdk.Participant) bool {
			return active.SID() == part.SID()
		})
	})
	for _, part := range inactive {
		camera, ok := part.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
		if !ok || camera == nil {
			continue
		}
		if camera.IsEnabled() {
			camera.SetEnabled(false)
		}
		e.cameraForgetDimensions(camera)
	}
}

// cameraRequestDimensions sends the tile size to the SFU when it changes, so
// the subscription receives the smallest simulcast layer covering the tile
// instead of the HIGH layer the SDK requests by default.
func (e *LivekitBin) cameraRequestDimensions(camera *lksdk.RemoteTrackPublication, width, height uint32) {
	dims := [2]uint32{width, height}

	e.cameraMu.Lock()
	unchanged := e.cameraDims[camera.SID()] == dims
	e.cameraDims[camera.SID()] = dims
	e.cameraMu.Unlock()

	if unchanged {
		return
	}
	camera.SetVideoDimensions(width, height)
}

func (e *LivekitBin) cameraForgetDimensions(camera *lksdk.RemoteTrackPublication) {
	e.cameraMu.Lock()
	delete(e.cameraDims, camera.SID())
	e.cameraMu.Unlock()
}
