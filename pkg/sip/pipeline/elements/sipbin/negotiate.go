package sipbin

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/livekit/protocol/livekit"
	"github.com/samber/lo"
)

const TrackSource_BFCP livekit.TrackSource = 5

func randID() string {
	return strconv.FormatInt(rand.Int63n(math.MaxInt16), 10)
}

func (e *SipBin) handleOfferSdp(self *gst.Bin, offerData []byte) ([]byte, error) {
	if e.ip == nil {
		return nil, fmt.Errorf("no IP address configured for SIP media")
	}

	// late offer
	if len(offerData) == 0 {
		return e.buildOfferSdp(self)
	}

	offer, err := gstsdp.ParseSDPMessage(string(offerData))
	if err != nil {
		return nil, err
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Received offer SDP:\n%s", offer.AsText()))

	answer, err := e.makeSdpSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create answer: %w", err)
	}

	medias := make([]*gstsdp.Media, offer.MediasLen())
	for i, media := range offer.Medias() {
		if media.GetPort() == 0 {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Media %d is disabled in answer, skipping", i))
			continue
		}
		switch kind := getMediaKind(media); kind {
		case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
			e.extractMediaCases(self, media, kind)
			if e.Tracks[kind] != nil {
				if e.Tracks[kind].Idx != i {
					self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received multiple media for track source %d, existing media index %d, new media index %d: disabling new media", kind, e.Tracks[kind].Idx, i))
					continue
				} else {
					self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Received duplicate media for track source %d, media index %d", kind, i))
					e.Tracks[kind].parseDirection(media)
					localMedia, err := e.makeTrackMedia(self, e.Tracks[kind], e.Tracks[kind].Caps)
					if err != nil {
						self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to make answer media for duplicate media index %d: %v", i, err))
						continue
					}
					medias[i] = localMedia
					self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Reusing existing track for duplicate media index %d with caps %s", i, e.Tracks[kind].Caps.String()))
					continue
				}
			}
			caps, err := e.selectCapsForMedia(self, media, kind)
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to select caps for media %d: %v", i, err))
				continue
			}
			track, err := e.NewTrack(self, i, kind, media.GetProto())
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create track for media %d: %v", i, err))
				continue
			}
			e.Tracks[kind] = track
			track.parseDirection(media)
			if ret := media.SetProto(track.Proto); ret != gstsdp.SDPResultOk {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set proto on media %d: %v", i, ret))
				continue
			}

			localMedia, err := e.makeTrackMedia(self, track, caps)
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to make answer media for media index %d: %v", i, err))
				continue
			}
			medias[i] = localMedia
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Added media %d to answer with caps %s", i, caps.String()))
			if err := track.Init(e, self, media, offer, caps); err != nil {
				medias[i] = nil
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize track for media %d: %v", i, err))
			}
			continue
		case TrackSource_BFCP:
			if e.Bfcp != nil {
				if e.Bfcp.Idx != i {
					self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received multiple media for BFCP track, existing media index %d, new media index %d: disabling new media", e.Bfcp.Idx, i))
					continue
				} else {
					self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Received duplicate media for BFCP track, media index %d", i))
					localMedia, err := e.makeBfcpMedia(e.Bfcp)
					if err != nil {
						self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to make answer media for duplicate BFCP media index %d: %v", i, err))
						continue
					}
					medias[i] = localMedia
					continue
				}
			}
			proto := media.GetProto()
			switch proto {
			case "UDP/BFCP":
			case "":
				proto = "UDP/BFCP"
			default:
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unsupported proto %s for BFCP media %d, disabling media", proto, i))
				continue
			}
			track, err := e.NewBfcpTrack(self, i, proto)
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to create BFCP track for media %d: %v", i, err))
				continue
			}
			e.Bfcp = track
			if err := track.Init(e, self, media, offer); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize BFCP track for media %d: %v", i, err))
				continue
			}
			localMedia, err := e.makeBfcpMedia(track)
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to make answer media for BFCP media %d: %v", i, err))
				continue
			}
			medias[i] = localMedia
			continue
		}
		medias[i] = nil
	}

	if err := e.bfcpMediaAddStreams(self, medias); err != nil {
		return nil, fmt.Errorf("failed to add BFCP streams: %w", err)
	}

	for i, media := range medias {
		if media == nil {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Disabling media %d because it is not compatible or supported: %s", i, offer.Media(i).AsText()))
			disabledMedia, err := disableMedia(offer.Media(i), answer)
			if err != nil {
				return nil, fmt.Errorf("failed to disable media %d: %w", i, err)
			}
			medias[i] = disabledMedia
		} else {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Adding media %d to answer", i))
			if ret := answer.AddMediaCopy(media); ret != gstsdp.SDPResultOk {
				return nil, fmt.Errorf("failed to add media %d to answer: %v", i, ret)
			}
		}
	}

	for i, track := range e.Tracks {
		if track != nil && (track.Idx >= len(medias) || medias[track.Idx].GetPort() == 0) {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up track for media index %d because it is disabled", i))
			if err := e.CleanupTrack(self, track); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup track for media index %d: %v", i, err))
			}
		}
	}
	if e.Bfcp != nil && (e.Bfcp.Idx >= len(medias) || medias[e.Bfcp.Idx].GetPort() == 0) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up BFCP track because media index %d is disabled in answer", e.Bfcp.Idx))
		if err := e.CleanupBfcp(self, e.Bfcp); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup BFCP track: %v", err))
		}
	}

	if answer.MediasLen() != offer.MediasLen() { // if you see that, then i did something very wrong in the code above
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Number of media in answer does not match offer: %d vs %d", answer.MediasLen(), offer.MediasLen()))
		self.Error("number of media in answer does not match offer", fmt.Errorf("%d vs %d", answer.MediasLen(), offer.MediasLen()))
		return nil, fmt.Errorf("number of media in answer does not match offer: %d vs %d", answer.MediasLen(), offer.MediasLen())
	}

	answerData := answer.AsText()
	if answerData == "" {
		return nil, fmt.Errorf("failed to serialize answer to text")
	}

	e.Medias = medias

	e.transaction.SetPending(TransactionPendingKindAck)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Generated answer SDP:\n%s", answerData))

	self.Log(CAT, gst.LevelDebug, "Emitting available media")
	e.emitAvailableMedia(self)

	self.Log(CAT, gst.LevelDebug, "Scheduling early reinvite if needed")
	e.earlyReinvite(self)

	self.Log(CAT, gst.LevelDebug, "Scheduling track cleanup for inactive tracks")
	e.clearTracks(self)

	self.Log(CAT, gst.LevelDebug, "Offer SDP processing complete")

	return []byte(answerData), nil
}

// offerReadyTimeout bounds how long an incoming offer waits for a pending
// transaction.
var offerReadyTimeout = 5 * time.Second

func (e *SipBin) OnOfferSdp(self *gst.Bin, offerData []byte) ([]byte, error) {
	unlock, err := e.transaction.WaitReadyTimeout(offerReadyTimeout)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to wait for transaction to be ready: %v", err))
		return nil, fmt.Errorf("transaction is not ready: %w", err)
	}
	defer unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	e.transactionID.Add(1)

	return e.handleOfferSdp(self, offerData)
}

func (e *SipBin) handleAnswerSdp(self *gst.Bin, answerData []byte) error {
	if e.ip == nil {
		return fmt.Errorf("no IP address configured for SIP media")
	}

	answer, err := gstsdp.ParseSDPMessage(string(answerData))
	if err != nil {
		return err
	}

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Received answer SDP:\n%s", answer.AsText()))

	medias := make([]*gstsdp.Media, answer.MediasLen())
	for i, media := range answer.Medias() {
		if i >= len(e.Medias) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received more media than expected in answer: media index %d exceeds expected media count %d, disabling media", i, len(e.Medias)))
			continue
		}
		if media.GetPort() == 0 {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Media %d is disabled in answer, skipping", i))
			continue
		}
		switch kind := getMediaKind(media); kind {
		case livekit.TrackSource_CAMERA, livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
			e.extractMediaCases(self, media, kind)
			if e.Tracks[kind] == nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No existing track for media %d with track source %d, disabling media", i, kind))
				continue
			}
			caps, err := e.selectCapsForMedia(self, media, kind)
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to select caps for media %d: %v", i, err))
				continue
			}
			e.Tracks[kind].parseDirection(media)
			localMedia, err := e.makeTrackMedia(self, e.Tracks[kind], caps)
			if err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to make answer media for media index %d: %v", i, err))
				continue
			}
			medias[i] = localMedia
			if err := e.Tracks[kind].Init(e, self, media, answer, caps); err != nil {
				medias[i] = nil
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize track for media %d: %v", i, err))
			}
			continue
		case TrackSource_BFCP:
			if e.Bfcp == nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No existing BFCP track for media %d, disabling media", i))
				continue
			}
			if err := e.Bfcp.Init(e, self, media, answer); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to initialize BFCP track for media %d: %v", i, err))
				continue
			}
			localMedia, err := e.makeBfcpMedia(e.Bfcp)
			if err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to make answer media for BFCP media %d: %v", i, err))
				continue
			}
			medias[i] = localMedia
			continue
		}
		medias[i] = nil
	}

	for i, media := range medias {
		if media == nil {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Disabling media %d because it is not compatible or supported: %s", i, answer.Media(i).AsText()))
			disabledMedia, err := disableMedia(answer.Media(i), nil)
			if err != nil {
				return fmt.Errorf("failed to disable media %d: %w", i, err)
			}
			medias[i] = disabledMedia
		}
	}

	for i, track := range e.Tracks {
		if track != nil && (track.Idx >= len(medias) || medias[track.Idx].GetPort() == 0) {
			self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up track for media index %d because it is disabled in answer", i))
			if err := e.CleanupTrack(self, track); err != nil {
				self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup track for media index %d: %v", i, err))
			}
		}
	}
	if e.Bfcp != nil && (e.Bfcp.Idx >= len(medias) || medias[e.Bfcp.Idx].GetPort() == 0) {
		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Cleaning up BFCP track because media index %d is disabled in answer", e.Bfcp.Idx))
		if err := e.CleanupBfcp(self, e.Bfcp); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup BFCP track: %v", err))
		}
	}

	e.Medias = medias

	self.Log(CAT, gst.LevelInfo, "Answer SDP processed successfully")

	e.emitAvailableMedia(self)

	e.clearTracks(self)

	return nil
}

func (e *SipBin) OnAnswerSdp(self *gst.Bin, answerData []byte) error {
	unlock, err := e.transaction.Ack(TransactionPendingKindAnswer)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to acknowledge transaction: %v", err))
		return fmt.Errorf("failed to acknowledge transaction: %w", err)
	}
	defer unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	e.pendingOffer = nil
	e.transactionID.Add(1)

	return e.handleAnswerSdp(self, answerData)
}

// pendingOffer records the media state added for an outgoing offer.
type pendingOffer struct {
	prevMediasLen int
}

// rollbackPendingOffer reverts the screenshare media/track added for the
// pending outgoing offer. Caller must hold e.mu.
func (e *SipBin) rollbackPendingOffer(self *gst.Bin) {
	po := e.pendingOffer
	if po == nil {
		return
	}
	e.pendingOffer = nil

	if po.prevMediasLen >= 0 && po.prevMediasLen < len(e.Medias) {
		e.Medias = e.Medias[:po.prevMediasLen]
	}

	if track := e.Tracks[livekit.TrackSource_SCREEN_SHARE]; track != nil && track.Idx >= po.prevMediasLen {
		if err := e.CleanupTrack(self, track); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to cleanup screenshare track during offer rollback: %v", err))
		}
	}

	// Remove the floorid attributes the offer added on the BFCP media.
	if e.Bfcp != nil && e.Bfcp.Idx < len(e.Medias) && e.Medias[e.Bfcp.Idx] != nil {
		bfcpMedia := e.Medias[e.Bfcp.Idx]
		for i := bfcpMedia.AttributesLen(); i > 0; {
			i--
			if bfcpMedia.GetAttribute(i).Key() != "floorid" {
				continue
			}
			if ret := bfcpMedia.RemoveAttribute(i); ret != gstsdp.SDPResultOk {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to remove floorid attribute during offer rollback: %v", ret))
			}
		}
	}

	self.Log(CAT, gst.LevelInfo, "Rolled back pending offer media state")
}

// OnOfferAborted releases the transaction held by an outgoing offer whose
// re-INVITE failed and rolls back the media state added for it.
func (e *SipBin) OnOfferAborted(self *gst.Bin) {
	unlock, err := e.transaction.Ack(TransactionPendingKindAnswer)
	if err != nil {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No pending offer transaction to abort: %v", err))
		return
	}
	defer unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	e.transactionID.Add(1)
	e.rollbackPendingOffer(self)
}

func (e *SipBin) buildOfferSdp(self *gst.Bin) ([]byte, error) {
	if e.Tracks[livekit.TrackSource_MICROPHONE] == nil {
		microphoneMedia, err := e.makeOfferMedia(self, livekit.TrackSource_MICROPHONE, len(e.Medias), "")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer media: %v", err))
			return nil, fmt.Errorf("failed to create offer media: %w", err)
		}
		e.Medias = append(e.Medias, microphoneMedia)
	}

	if e.Tracks[livekit.TrackSource_CAMERA] == nil {
		cameraMedia, err := e.makeOfferMedia(self, livekit.TrackSource_CAMERA, len(e.Medias), "")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer media: %v", err))
			return nil, fmt.Errorf("failed to create offer media: %w", err)
		}
		e.Medias = append(e.Medias, cameraMedia)
	}

	if e.Tracks[livekit.TrackSource_SCREEN_SHARE] == nil {
		screenshareMedia, err := e.makeOfferMedia(self, livekit.TrackSource_SCREEN_SHARE, len(e.Medias), "")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer media: %v", err))
			return nil, fmt.Errorf("failed to create offer media: %w", err)
		}
		e.Medias = append(e.Medias, screenshareMedia)
	}

	if e.Bfcp == nil {
		bfcpTrack, err := e.NewBfcpTrack(self, len(e.Medias), "UDP/BFCP")
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create BFCP track: %v", err))
			return nil, fmt.Errorf("failed to create BFCP track: %w", err)
		}
		e.Bfcp = bfcpTrack
		bfcpMedia, err := e.makeBfcpMedia(bfcpTrack)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer media: %v", err))
			return nil, fmt.Errorf("failed to create offer media: %w", err)
		}
		e.Medias = append(e.Medias, bfcpMedia)
	}

	if err := e.bfcpMediaAddStreams(self, e.Medias); err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add BFCP streams to offer: %v", err))
		return nil, fmt.Errorf("failed to add BFCP streams to offer: %w", err)
	}

	offer, err := e.makeOfferSdp(self)
	if err != nil {
		self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer: %v", err))
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}

	offerData := offer.AsText()
	if offerData == "" {
		self.Log(CAT, gst.LevelError, "Failed to serialize offer")
		return nil, fmt.Errorf("failed to serialize offer")
	}

	e.transaction.SetPending(TransactionPendingKindAck)

	self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Generated offer SDP:\n%s", offerData))

	return []byte(offerData), nil
}

func (e *SipBin) makeOfferSdp(self *gst.Bin) (*gstsdp.Message, error) {
	offer, err := e.makeSdpSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}

	for idx, media := range e.Medias {
		if media == nil {
			return nil, fmt.Errorf("media is nil at index %d", idx)
		}
		if ret := offer.AddMediaCopy(media); ret != gstsdp.SDPResultOk {
			return nil, fmt.Errorf("failed to add media at index %d to offer: %v", idx, ret)
		}
	}

	return offer, nil
}

func (e *SipBin) makeSdpSession() (*gstsdp.Message, error) {
	session, err := gstsdp.NewMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to create answer: %w", err)
	}

	if ret := session.SetVersion("0"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set version on answer: %v", ret)
	}
	e.sdpVersion++
	if ret := session.SetOrigin("-", e.sessionID, strconv.FormatUint(e.sdpVersion, 10), "IN", lo.Ternary(e.ip.To4() != nil, "IP4", "IP6"), e.ip.String()); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set origin on answer: %v", ret)
	}
	if ret := session.SetSessionName("LiveKit SIP"); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set session name on answer: %v", ret)
	}
	if ret := session.AddTime("0", "0", nil); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set timing on answer: %v", ret)
	}
	if ret := session.SetConnection("IN", lo.Ternary(e.ip.To4() != nil, "IP4", "IP6"), e.ip.String(), 0, 0); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set connection on answer: %v", ret)
	}

	return session, nil
}

func disableMedia(media *gstsdp.Media, answer *gstsdp.Message) (*gstsdp.Media, error) {
	newMedia, err := media.Copy()
	if err != nil {
		return nil, fmt.Errorf("failed to copy media: %w", err)
	}
	if newMedia.SetPortInfo(0, 0) != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("could not set port to 0 for media %s: %s", media.GetMedia(), media.AsText())
	}

	i := newMedia.AttributesLen()
	for i > 0 {
		i--
		attr := newMedia.GetAttribute(i)
		switch attr.Key() {
		case "direction", "recvonly", "sendrecv", "sendonly", "inactive":
			if ret := newMedia.RemoveAttribute(i); ret != gstsdp.SDPResultOk {
				return nil, fmt.Errorf("could not remove attribute %s from media %s: %v", attr.Key(), media.GetMedia(), ret)
			}
		}
	}

	if ret := newMedia.AddAttribute("inactive", ""); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("could not add inactive attribute to media %s: %v", media.GetMedia(), ret)
	}

	if answer != nil {
		if ret := answer.AddMediaCopy(newMedia); ret != gstsdp.SDPResultOk {
			return nil, fmt.Errorf("could not add media to answer: %v", ret)
		}
	}

	return newMedia, nil
}

func getMediaKind(media *gstsdp.Media) livekit.TrackSource {
	switch media.GetMedia() {
	case "audio":
		return livekit.TrackSource_MICROPHONE
	case "video":
		if media.GetAttributeVal("content") == "slides" {
			return livekit.TrackSource_SCREEN_SHARE
		}
		return livekit.TrackSource_CAMERA
	case "application":
		proto := media.GetProto()
		formats := lo.Map(strings.Split(proto, "/"), func(s string, _ int) string {
			return strings.ToUpper(strings.TrimSpace(s))
		})
		if lo.LastOr(formats, "") == "BFCP" && lo.Contains(formats, "UDP") {
			return TrackSource_BFCP
		}
	}
	return livekit.TrackSource_UNKNOWN
}

func (e *SipBin) earlyReinvite(self *gst.Bin) {
	if e.Bfcp == nil || e.Tracks[livekit.TrackSource_SCREEN_SHARE] != nil || e.Tracks[livekit.TrackSource_CAMERA] == nil {
		return
	}

	if !self.SignalHasHandlerPending(SignalSendOfferSdpID, glib.Quark(0), true) {
		self.Log(CAT, gst.LevelWarning, "No handler for send-offer-sdp signal, cannot send early reinvite")
		return
	}

	weakself := glib.WeakRefInit(self)
	weake := weak.Make(e)

	transactionID := e.transactionID.Load() + 1

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()

		time.Sleep(500 * time.Millisecond) // give time to device for internal processing. should not be necessary but sip implementation are broken half of the time

		self := gst.ToGstBin(weakself.Get())
		e := weake.Value()
		if self == nil || self.Instance() == nil || e == nil {
			return
		}

		unlock, err := e.transaction.WaitReady()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to start SIP transaction for sending offer: %v", err))
			return
		}
		defer unlock()

		e.mu.Lock()
		defer e.mu.Unlock()

		if e.transactionID.Load() != transactionID {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Not sending early reinvite offer because transaction ID has changed: current %d, expected %d", e.transactionID.Load(), transactionID))
			return
		}
		e.transactionID.Add(1)

		prevMediasLen := len(e.Medias)
		screenshareMedia, err := e.makeOfferMedia(self, livekit.TrackSource_SCREEN_SHARE, prevMediasLen, e.Tracks[livekit.TrackSource_CAMERA].Proto)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer media for early reinvite: %v", err))
			return
		}

		e.Medias = append(e.Medias, screenshareMedia)
		e.pendingOffer = &pendingOffer{prevMediasLen: prevMediasLen}

		if err := e.bfcpMediaAddStreams(self, e.Medias); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to add BFCP streams to offer: %v", err))
			e.rollbackPendingOffer(self)
			return
		}

		offer, err := e.makeOfferSdp(self)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to create offer for early reinvite: %v", err))
			e.rollbackPendingOffer(self)
			return
		}

		offerData := offer.AsText()
		if offerData == "" {
			self.Log(CAT, gst.LevelError, "Failed to serialize offer for early reinvite")
			e.rollbackPendingOffer(self)
			return
		}

		self.Log(CAT, gst.LevelInfo, fmt.Sprintf("Generated offer SDP:\n%s", string(offerData)))

		if _, err := self.Emit("send-offer-sdp", string(offerData)); err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to emit send-offer-sdp signal: %v", err))
			e.rollbackPendingOffer(self)
			return
		}
		e.transaction.SetPending(TransactionPendingKindAnswer)
		self.Log(CAT, gst.LevelInfo, "Early reinvite offer sent successfully")
	}()
}

func (e *SipBin) clearTracks(self *gst.Bin) {
	var toClear []*SipTrack
	for _, track := range e.Tracks {
		if track != nil && !track.recv {
			toClear = append(toClear, track)
		}
	}

	if len(toClear) <= 0 {
		return
	}

	transactionID := e.transactionID.Load() + 1

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		unlock, err := e.transaction.WaitReady()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Failed to start SIP transaction for sending offer: %v", err))
			return
		}
		defer unlock()

		e.mu.Lock()
		defer e.mu.Unlock()

		if e.transactionID.Load() != transactionID {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Not cleaning up tracks because transaction ID has changed: current %d, expected %d", e.transactionID.Load(), transactionID))
			return
		}
		e.transactionID.Add(1)

		wg := sync.WaitGroup{}
		for _, track := range toClear {
			if track == nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.clearTrack(self, track.Kind)
			}()
		}
		wg.Wait()
	}()
}
