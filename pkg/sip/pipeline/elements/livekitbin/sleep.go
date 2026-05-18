package livekitbin

import (
	"fmt"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/samber/lo"
)

func (e *LivekitBin) cameraSleep(self *gst.Bin, p []lksdk.Participant) {
	if !e.config.camera {
		return
	}

	if e.maxActiveParticipants == 0 {
		return
	}

	for _, part := range p {
		rp, ok := part.(*lksdk.RemoteParticipant)
		if !ok {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Participant %s is not a remote participant", part.Identity()))
			continue
		}
		camera, ok := rp.GetTrackPublication(livekit.TrackSource_CAMERA).(*lksdk.RemoteTrackPublication)
		if !ok || camera == nil {
			continue
		}

		if !camera.IsSubscribed() {
			if err := camera.SetSubscribed(true); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to subscribe to camera track for participant %s: %v", rp.Identity(), err))
			}
		}

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
	}
}
