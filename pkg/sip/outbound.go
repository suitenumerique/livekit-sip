// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sip

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/icholy/digest"
	"github.com/pkg/errors"
	"golang.org/x/exp/maps"

	"github.com/livekit/media-sdk/dtmf"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/guid"
	"github.com/livekit/protocol/utils/traceid"
	"github.com/livekit/psrpc"
	"github.com/livekit/sipgo"
	"github.com/livekit/sipgo/sip"

	"github.com/livekit/sip/pkg/config"
	"github.com/livekit/sip/pkg/stats"
)

type sipOutboundConfig struct {
	address         string
	transport       livekit.SIPTransport
	host            string
	from            string
	to              string
	user            string
	pass            string
	dtmf            string
	dialtone        bool
	headers         map[string]string
	includeHeaders  livekit.SIPHeaderOptions
	headersToAttrs  map[string]string
	attrsToHeaders  map[string]string
	ringingTimeout  time.Duration
	maxCallDuration time.Duration
	enabledFeatures []livekit.SIPFeature
	featureFlags    map[string]string
	mediaConfig     *sipMediaConfig
	displayName     *string
}

type outboundCall struct {
	c         *Client
	tid       traceid.ID
	log       logger.Logger
	state     *CallState
	callStart time.Time
	cc        *sipOutbound
	// media     *MediaPort
	medias *MediaOrchestrator
	// started   core.Fuse
	stopped   core.Fuse
	closing   core.Fuse
	stats     Stats
	sigTs     SignalingTimestamps
	jitterBuf bool
	projectID string

	mu  sync.RWMutex
	mon *stats.CallMonitor
	// lkRoom   RoomInterface
	// lkRoomIn msdk.PCM16Writer // output to room; OPUS at 48k
	sipConf sipOutboundConfig
}

func (c *Client) newCall(ctx context.Context, tid traceid.ID, conf *config.Config, log logger.Logger, id LocalTag, room RoomConfig, sipConf sipOutboundConfig, state *CallState, projectID string) (*outboundCall, error) {
	signalLoggingEnabled, _ := strconv.ParseBool(sipConf.featureFlags[signalLoggingFeatureFlag])
	if sipConf.maxCallDuration <= 0 || sipConf.maxCallDuration > maxCallDuration {
		sipConf.maxCallDuration = maxCallDuration
	}
	if sipConf.ringingTimeout <= 0 {
		sipConf.ringingTimeout = defaultRingingTimeout
	}
	jitterBuf := SelectValueBool(conf.EnableJitterBuffer, conf.EnableJitterBufferProb)
	room.JitterBuf = jitterBuf
	room.LogSignalChanges = signalLoggingEnabled

	tr := TransportFrom(sipConf.transport)
	contact := c.ContactURI(tr)
	if sipConf.host == "" {
		sipConf.host = contact.GetHost()
	}
	fromURI := URI{
		User:      sipConf.from,
		Host:      sipConf.host,
		Addr:      contact.Addr,
		Transport: tr,
	}
	now := time.Now()
	call := &outboundCall{
		c:         c,
		tid:       tid,
		log:       log,
		sipConf:   sipConf,
		state:     state,
		callStart: now,
		sigTs:     SignalingTimestamps{APITime: now},
		// jitterBuf: jitterBuf,
		projectID: projectID,
	}
	call.stats.Update()
	call.cc = c.newOutbound(log, id, fromURI, contact, sipConf.displayName, call.setAttrsToHeaders)
	call.log = call.log.WithValues("jitterBuf", call.jitterBuf, "sipCallID", call.cc.callID)
	if sipConf.featureFlags[outboundRouteHeadersFeatureFlag] == "true" {
		call.cc.routeHeaders = conf.OutboundRouteHeaders
	}

	call.mon = c.mon.NewCall(stats.Outbound, sipConf.host, sipConf.address)
	var err error

	// call.media, err = NewMediaPort(tid, call.log, call.mon, &MediaOptions{
	// 	IP:                   c.sconf.MediaIP,
	// 	Ports:                conf.RTPPort,
	// 	MediaTimeoutInitial:  c.conf.MediaTimeoutInitial,
	// 	MediaTimeout:         sipConf.mediaConfig.MediaTimeout,
	// 	SymmetricRTP:         c.conf.SymmetricRTP,
	// 	IgnoreLocalAddrInSDP: c.conf.IgnoreLocalAddrInSDP,
	// 	EnableJitterBuffer:   call.jitterBuf,
	// 	LogSignalChanges:     signalLoggingEnabled,
	// 	Stats:                &call.stats.Port,
	// 	NoInputResample:      !RoomResample,
	// 	IgnorePreanswerData:  true,
	// }, RoomSampleRate)

	opts := &MediaOptions{
		IP:                    c.sconf.MediaIP,
		IPLocal:               c.sconf.MediaIPLocal,
		Ports:                 conf.RTPPort,
		MediaTimeoutInitial:   c.conf.MediaTimeoutInitial,
		MediaTimeout:          c.conf.MediaTimeout,
		EnableJitterBuffer:    true,
		NoInputResample:       !RoomResample,
		VideoWidth:            uint(c.conf.Video.Width),
		VideoHeight:           uint(c.conf.Video.Height),
		Framerate:             uint(c.conf.Video.Framerate),
		Lang:                  c.conf.Lang,
		MaxActiveParticipants: c.conf.MaxActiveParticipants,
		Gst:                   c.conf.Gst,
		PublishCodecs:         c.conf.PublishCodecs,
	}

	// The media orchestrator (and its GStreamer pipeline) must live for the
	// duration of the call, not the CreateSIPParticipant request. The request
	// ctx is cancelled by PSRPC as soon as the RPC returns; if we let that
	// propagate into the orchestrator, o.ctx.Done() fires immediately after the
	// call is established and loopEvents() tears the pipeline down (observed as
	// "Outbound SIP call established" instantly followed by "Closing pipeline").
	// The orchestrator owns its own cancel(), driven by the call's close() path.
	// The inbound path passes a call-lifetime ctx (c.ctx) for the same reason.
	call.medias, err = NewMediaOrchestrator(log, context.WithoutCancel(ctx), call.cc, call.cc.callID, opts)
	if err != nil {
		call.close(ctx, errors.Wrap(err, "media failed"), callDropped, stats.ServerError("media-failed"), livekit.DisconnectReason_UNKNOWN_REASON)
		return nil, err
	}
	// call.media.SetDTMFAudio(conf.AudioDTMF)
	// call.media.EnableTimeout(false)
	// call.media.DisableOut() // disabled until we get 200
	if err := call.connectToRoom(ctx, room); err != nil {
		call.close(ctx, errors.Wrap(err, "room join failed"), callDropped, stats.ServerError("join-failed"), livekit.DisconnectReason_UNKNOWN_REASON)
		return nil, psrpc.NewError(psrpc.Internal, fmt.Errorf("update room failed: %w", err))
	}

	c.cmu.Lock()
	defer c.cmu.Unlock()
	c.activeCalls[id] = call
	return call, nil
}

func (c *outboundCall) setAttrsToHeaders(headers map[string]string) map[string]string {
	if len(c.sipConf.attrsToHeaders) == 0 {
		return headers
	}
	// r := c.lkRoom.Room()
	// if r == nil {
	// 	return headers
	// }
	return AttrsToHeaders(c.medias.Attributes(), c.sipConf.attrsToHeaders, headers)
}

func (c *outboundCall) ensureClosed(ctx context.Context) {
	c.state.Update(ctx, func(info *livekit.SIPCallInfo) {
		if info.Error != "" {
			info.CallStatus = livekit.SIPCallStatus_SCS_ERROR
		} else {
			info.CallStatus = livekit.SIPCallStatus_SCS_DISCONNECTED
		}
		if c.medias != nil {
			info.ParticipantIdentity = c.medias.ParticipantIdentity()
			info.ParticipantAttributes = c.medias.Attributes()
		}
		info.EndedAtNs = time.Now().UnixNano()
	})
}

func (c *outboundCall) setErrStatus(ctx context.Context, err error) {
	if err == nil {
		return
	}
	c.state.Update(ctx, func(info *livekit.SIPCallInfo) {
		if info.Error != "" {
			return
		}
		info.Error = err.Error()
		info.CallStatus = livekit.SIPCallStatus_SCS_ERROR
	})
}

func (c *outboundCall) Dial(ctx context.Context) error {
	ctx, span := Tracer.Start(ctx, "sip.outbound.Dial")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, c.sipConf.maxCallDuration)
	defer cancel()
	c.mon.CallStart()
	defer c.mon.CallEnd()

	err := c.connectSIP(ctx, c.tid)
	if err != nil {
		c.ensureClosed(ctx)
		return err // connectSIP updates the error code on the callInfo
	}

	c.state.Update(ctx, func(info *livekit.SIPCallInfo) {
		if c.medias == nil {
			c.log.Errorw("failed to update SIP info", fmt.Errorf("unexpected state: lkroom is not set"))
			return
		}
		info.RoomId = c.medias.RoomName()
		info.StartedAtNs = time.Now().UnixNano()
		info.CallStatus = livekit.SIPCallStatus_SCS_ACTIVE
	})
	return nil
}

func (c *outboundCall) WaitClose(ctx context.Context) error {
	ctx, span := Tracer.Start(ctx, "sip.outbound.WaitClose")
	defer span.End()
	return c.waitClose(ctx, c.tid)
}
func (c *outboundCall) waitClose(ctx context.Context, tid traceid.ID) error {
	ctx = context.WithoutCancel(ctx)
	defer c.ensureClosed(ctx)

	ticker := time.NewTicker(stateUpdateTick)
	defer ticker.Stop()

	statsTicker := time.NewTicker(statsInterval)
	defer statsTicker.Stop()
	for {
		select {
		case <-statsTicker.C:
			c.stats.Update()
			c.printStats()
		case <-ticker.C:
			c.log.Debugw("sending keep-alive")
			c.state.ForceFlush(ctx)
		case <-c.Disconnected():
			// term := terminationFromRoomDisconnect(c.lkRoom.ClosedReason())
			// c.CloseWithReason(ctx, callDropped, term, livekit.DisconnectReason_CLIENT_INITIATED)
			c.close(ctx, nil, callDropped, stats.ClientError("disconnected"), livekit.DisconnectReason_CLIENT_INITIATED)
			return nil
		case <-c.cc.callTimeout.C:
			c.closeWithTimeout(ctx)
			err := psrpc.NewErrorf(psrpc.DeadlineExceeded, "media timeout")
			c.setErrStatus(ctx, err)
			return err
		case <-c.Closed():
			return nil
		}
	}
}

func (c *outboundCall) DialAsync(ctx context.Context) {
	ctx, span := Tracer.Start(ctx, "sip.outbound.DialAsync")
	defer span.End()
	ctx = context.WithoutCancel(ctx)
	go func() {
		if err := c.Dial(ctx); err != nil {
			return
		}
		_ = c.WaitClose(ctx)
	}()
}

func (c *outboundCall) Closed() <-chan struct{} {
	return c.stopped.Watch()
}

func (c *outboundCall) Disconnected() <-chan struct{} {
	return c.medias.Closed()
}

func (c *outboundCall) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.close(ctx, nil, callDropped, stats.ServerError("shutdown"), livekit.DisconnectReason_SERVER_SHUTDOWN)
	return nil
}

func (c *outboundCall) CloseWithReason(ctx context.Context, status CallStatus, t stats.Termination, reason livekit.DisconnectReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.close(ctx, nil, status, t, reason)
}

func (c *outboundCall) closeWithTimeout(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.close(ctx, psrpc.NewErrorf(psrpc.DeadlineExceeded, "media-timeout"), callDropped, stats.ServerError("media-timeout"), livekit.DisconnectReason_UNKNOWN_REASON)
}

func (c *outboundCall) printStats() {
	c.stats.Log(c.log, c.callStart)
}

func (c *outboundCall) close(ctx context.Context, err error, status CallStatus, t stats.Termination, reason livekit.DisconnectReason) {
	c.closing.Break()
	ctx = context.WithoutCancel(ctx)
	c.stopped.Once(func() {
		c.stats.Closed.Store(true)
		log := c.log.WithValues("status", status, "result", string(t.Result), "reason", t.Reason)
		defer func() {
			c.stats.Update()
			c.printStats()
			c.sigTs.Log(log)
		}()

		c.setStatus(status)
		if err != nil {
			log.Warnw("Closing outbound call with error", nil)
		} else {
			log.Infow("Closing outbound call")
		}
		c.state.Update(ctx, func(info *livekit.SIPCallInfo) {
			if err != nil && info.Error == "" {
				info.Error = err.Error()
				info.CallStatus = livekit.SIPCallStatus_SCS_ERROR
			}
			info.DisconnectReason = reason
		})

		// Send BYE _before_ closing media/room connection.
		// This ensures participant attributes are still available for
		// attributes_to_headers mapping in the setHeaders callback.
		// See: https://github.com/livekit/sip/issues/404
		c.stopSIP(ctx, t)
		c.medias.Close()
		c.medias = nil
		// if r := c.lkRoom; r != nil {
		// 	_ = r.CloseOutput()
		// 	_ = r.CloseWithReason(status.DisconnectReason())
		// }
		// c.lkRoomIn = nil

		c.c.cmu.Lock()
		delete(c.c.activeCalls, c.cc.ID())
		c.c.cmu.Unlock()

		c.c.DeregisterTransferSIPParticipant(string(c.cc.ID()))

		// Call the handler asynchronously to avoid blocking
		if c.c.handler != nil {
			go func(tid traceid.ID) {
				ctx := context.WithoutCancel(ctx)
				ctx, span := Tracer.Start(ctx, "sip.outbound.OnSessionEnd")
				defer span.End()
				c.c.handler.OnSessionEnd(ctx, &CallIdentifier{
					TraceID:   tid,
					ProjectID: c.projectID,
					CallID:    c.state.callInfo.CallId,
					SipCallID: c.cc.SIPCallID(),
				}, c.state.callInfo, t.Reason)
			}(c.tid)
		}
	})
}

func (c *outboundCall) Participant() ParticipantInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.medias.Participant()
}

func (c *outboundCall) connectSIP(ctx context.Context, tid traceid.ID) error {
	ctx, span := Tracer.Start(ctx, "sip.outbound.connectSIP")
	defer span.End()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.dialSIP(ctx, tid); err != nil {
		c.log.Infow("SIP call failed", "error", err)
		res := classifyInviteError(err)
		c.close(ctx, res.reportErr, res.status, res.term, res.reason)
		return res.returnErr
	}
	c.connectMedia()
	// c.started.Break()
	if err := c.medias.Start(); err != nil {
		c.log.Infow("failed to start media", "error", err)
		c.close(ctx, errors.Wrap(err, "failed to start media"), callDropped, stats.ServerError("media-start-failed"), livekit.DisconnectReason_UNKNOWN_REASON)
		return err
	}
	c.log.Infow("Outbound SIP call established")
	return nil
}

func (c *outboundCall) connectToRoom(ctx context.Context, lkNew RoomConfig) error {
	ctx, span := Tracer.Start(ctx, "sip.outbound.connectToRoom")
	defer span.End()
	attrs := lkNew.Participant.Attributes
	if attrs == nil {
		attrs = make(map[string]string)
	}

	sipCallID := attrs[livekit.AttrSIPCallID]
	if sipCallID != "" {
		c.c.RegisterTransferSIPParticipant(sipCallID, c)
	}

	attrs[livekit.AttrSIPCallStatus] = CallDialing.Attribute()
	lkNew.Participant.Attributes = attrs
	if err := c.medias.Connect(c.c.conf, lkNew); err != nil {
		return err
	}
	// // We have to create the track early because we might play a dialtone while SIP connects.
	// // Thus, we are forced to set full sample rate here instead of letting the codec adapt to the SIP source sample rate.
	// local, err := r.NewParticipantTrack(RoomSampleRate)
	// if err != nil {
	// 	_ = r.Close()
	// 	return err
	// }
	// c.lkRoom = r
	// c.lkRoomIn = local
	// if err := registerSignalingRPC(c.lkRoom, c.cc); err != nil {
	// 	return err
	// }
	return nil
}

func (c *outboundCall) dialSIP(ctx context.Context, tid traceid.ID) error {
	// if c.sipConf.dialtone {
	// 	const ringVolume = math.MaxInt16 / 2
	// 	rctx, rcancel := context.WithCancel(ctx)
	// 	defer rcancel()

	// 	dst := c.lkRoomIn // already under mutex

	// 	// Play dialtone to the room while participant connects
	// 	go func(tid traceid.ID) {
	// 		rctx, span := Tracer.Start(rctx, "tones.Play")
	// 		defer span.End()

	// 		if dst == nil {
	// 			c.log.Infow("room is not ready, ignoring dial tone")
	// 			return
	// 		}
	// 		err := tones.Play(rctx, dst, ringVolume, tones.ETSIRinging)
	// 		if err != nil && !errors.Is(err, context.Canceled) {
	// 			c.log.Infow("cannot play dial tone", "error", err)
	// 		}
	// 	}(tid)
	// }
	err := c.sipSignal(ctx, tid)
	if err != nil {
		return err
	}

	// if digits := c.sipConf.dtmf; digits != "" {
	// 	c.setStatus(CallAutomation)
	// 	// Write initial DTMF to SIP
	// 	if err := c.media.WriteDTMF(ctx, digits); err != nil {
	// 		return err
	// 	}
	// }
	c.setStatus(CallActive)

	return nil
}

func (c *outboundCall) connectMedia() {
	// if w := c.lkRoom.SwapOutput(c.media.GetAudioWriter()); w != nil {
	// 	_ = w.Close()
	// }
	// c.lkRoom.SetDTMFOutput(c.media)

	// c.media.WriteAudioTo(c.lkRoomIn)
	c.medias.DtmfHandler(c.handleDTMF)
}

type sipRespFunc func(code sip.StatusCode, hdrs Headers)

func sipResponse(ctx context.Context, tx sip.ClientTransaction, stop <-chan struct{}, setState sipRespFunc) (*sip.Response, error) {
	cnt := 0
	for {
		select {
		case <-ctx.Done():
			_ = tx.Cancel()
			// NOTE: psrpc.Canceled does not auto-retry, whereas psrpc.DeadlineExceeded does
			// As long as that is the case, avoid psrpc.DeadlineExceeded to prevent hammering of destination.
			return nil, psrpc.NewError(psrpc.Canceled, ErrSIPRequestTimeout)
		case <-stop:
			_ = tx.Cancel()
			return nil, psrpc.NewErrorf(psrpc.Canceled, "service shutting down")
		case <-tx.Done():
			return nil, psrpc.NewErrorf(psrpc.Canceled, "transaction failed to complete (%d intermediate responses)", cnt)
		case res := <-tx.Responses():
			status := res.StatusCode
			if setState != nil {
				setState(res.StatusCode, res.Headers())
			}
			if status/100 != 1 { // != 1xx
				return res, nil
			}
			// continue
			cnt++
		}
	}
}

func (c *outboundCall) stopSIP(ctx context.Context, t stats.Termination) {
	termCtx, cancel := context.WithCancel(context.Background()) // Do not use ctx
	defer cancel()
	go func() {
		select {
		case <-termCtx.Done():
			return
		case <-time.After(5 * time.Minute):
			c.mon.CallTerminationFailure()
			c.log.Errorw("call failed to terminate after 5 minutes", nil) // To be able to get call IDs
		}
	}()

	c.mon.CallTerminate(t)
	c.cc.Close(ctx)
}

func (c *outboundCall) setStatus(v CallStatus) {
	attr := v.Attribute()
	if attr == "" {
		return
	}
	// if r == nil {
	// 	return
	// }
	// r.LocalParticipant.SetAttributes(map[string]string{
	// 	livekit.AttrSIPCallStatus: attr,
	// })
	if c.medias == nil {
		return
	}
	if err := c.medias.SetAttributes(map[string]string{
		livekit.AttrSIPCallStatus: attr,
	}); err != nil {
		c.log.Errorw("failed to set attributes", err)
	}
}

func (c *outboundCall) setExtraAttrs(hdrToAttr map[string]string, opts livekit.SIPHeaderOptions, cc Signaling, hdrs Headers) {
	extra := HeadersToAttrs(nil, hdrToAttr, opts, cc, hdrs)
	if c.medias != nil && len(extra) != 0 {
		// room := c.lkRoom.Room()
		// if room != nil {
		// 	room.LocalParticipant.SetAttributes(extra)
		// } else {
		// 	c.log.Warnw("could not set attributes on nil room", nil, "attrs", extra)
		// }

		if err := c.medias.SetAttributes(extra); err != nil {
			c.log.Errorw("failed to set attributes", err)
		}
	}
}

func (c *outboundCall) sipSignal(ctx context.Context, tid traceid.ID) error {
	ctx, span := Tracer.Start(ctx, "sip.outbound.sipSignal")
	defer span.End()

	if c.sipConf.ringingTimeout > 0 {
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, c.sipConf.ringingTimeout)
		defer cancel()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			// parent context cancellation or success
			return
		case <-c.Disconnected():
		case <-c.Closed():
		}
		cancel()
	}()

	// mconf := c.sipConf.mediaConfig
	sdpOfferData, err := c.medias.NewOffer()
	if err != nil {
		return err
	}
	// c.log.Infow("Generated SDP offer", "sdp", string(sdpOfferData))
	// sdpOfferData, err := sdpOffer.SDP.Marshal()
	// if err != nil {
	// 	return err
	// }
	c.mon.SDPSize(len(sdpOfferData), true)
	c.log.Debugw("SDP offer", "sdp", string(sdpOfferData))
	joinDur := c.mon.JoinDur()

	c.mon.InviteReq()
	c.sigTs.InviteTime = time.Now()

	toUri := CreateURIFromUserAndAddress(c.sipConf.to, c.sipConf.address, TransportFrom(c.sipConf.transport))

	ringing := false
	sdpResp, err := c.cc.Invite(ctx, toUri, c.sipConf.user, c.sipConf.pass, c.sipConf.headers, sdpOfferData, func(code sip.StatusCode, hdrs Headers) {
		if code == sip.StatusOK {
			return // is set separately
		}
		if code == sip.StatusTrying && c.sigTs.TryingTime.IsZero() {
			c.sigTs.TryingTime = time.Now()
		}
		if !ringing && code >= sip.StatusRinging && code < sip.StatusOK {
			ringing = true
			c.sigTs.RingingTime = time.Now()
			c.setStatus(CallRinging)
		}
		c.setExtraAttrs(nil, 0, nil, hdrs)
	})
	// Update SIPCallInfo with the SIP Call-ID after Invite
	if sipCallID := c.cc.SIPCallID(); sipCallID != "" {
		c.state.DeferUpdate(func(info *livekit.SIPCallInfo) {
			info.SipCallId = sipCallID
			// Set callidfull in participant attributes for backwards compatibility
			if info.ParticipantAttributes == nil {
				info.ParticipantAttributes = make(map[string]string)
			}
			info.ParticipantAttributes[AttrSIPCallIDFull] = sipCallID
		})
	}
	if err != nil {
		// TODO: should we retry? maybe new offer will work
		var e *livekit.SIPStatus
		if errors.As(err, &e) {
			c.mon.InviteError(statusName(int(e.Code)))
			c.state.DeferUpdate(func(info *livekit.SIPCallInfo) {
				info.CallStatusCode = e
			})
		} else {
			c.mon.InviteError("other")
		}
		c.cc.Close(ctx)
		c.log.Infow("SIP invite failed", "error", err)
		return err
	}
	c.sigTs.AcceptTime = time.Now()
	c.mon.SDPSize(len(sdpResp), false)
	c.log.Debugw("SDP answer", "sdp", string(sdpResp))

	c.log = LoggerWithHeaders(c.log, c.cc)

	err = c.medias.SetAnswerSDP(sdpResp)
	if err != nil {
		return err
	}
	// if err = c.media.SetConfig(mc); err != nil {
	// 	return err
	// }
	// mc.Processor = c.c.handler.GetMediaProcessor(c.sipConf.enabledFeatures, c.sipConf.featureFlags, string(c.cc.ID()), MediaProcessorOpts{InputSampleRate: c.media.InputSampleRate()})
	c.cc.SetLocalSDP(sdpOfferData)

	c.mon.InviteAccept()
	// c.media.EnableOut()
	// c.media.EnableTimeout(true)
	err = c.cc.AckInviteOK(ctx)
	if err != nil {
		c.log.Infow("SIP accept failed", "error", err)
		return err
	}
	c.sigTs.AckTime = time.Now()
	joinDur()

	// Dialog confirmed (ACK sent): start the RFC 4028 session-timer refresh
	c.cc.accepted(ctx)

	c.setExtraAttrs(c.sipConf.headersToAttrs, c.sipConf.includeHeaders, c.cc, nil)
	c.state.DeferUpdate(func(info *livekit.SIPCallInfo) {
		// info.AudioCodec = mc.Audio.Codec.Info().SDPName
		// if r := c.lkRoom.Room(); r != nil {
		// 	info.ParticipantAttributes = r.LocalParticipant.Attributes()
		// }
		if c.medias != nil {
			info.ParticipantAttributes = c.medias.Attributes()
		}
	})
	return nil
}

func (c *outboundCall) handleDTMF(ev dtmf.Event) {
	// if c.lkRoom == nil {
	// 	return
	// }
	// _ = c.lkRoom.SendData(&livekit.SipDTMF{
	// 	Code:  uint32(ev.Code),
	// 	Digit: string([]byte{ev.Digit}),
	// }, lksdk.WithDataPublishReliable(true))
}

func (c *outboundCall) transferCall(ctx context.Context, transferTo string, headers map[string]string, dialtone bool) (retErr error) {
	ctx, span := Tracer.Start(ctx, "sip.outbound.transferCall")
	defer span.End()
	var err error

	tID := c.state.StartTransfer(ctx, transferTo)
	defer func() {
		c.state.EndTransfer(ctx, tID, retErr)
	}()

	// if dialtone && c.started.IsBroken() && !c.stopped.IsBroken() {
	// 	const ringVolume = math.MaxInt16 / 2
	// 	rctx, rcancel := context.WithCancel(ctx)
	// 	defer rcancel()

	// 	// mute the room audio to the SIP participant
	// 	// w := c.lkRoom.SwapOutput(nil)

	// 	defer func() {
	// 		if retErr != nil && !c.stopped.IsBroken() {
	// 			c.lkRoom.SwapOutput(w)
	// 		} else {
	// 			w.Close()
	// 		}
	// 	}()

	// 	go func() {
	// 		aw := c.media.GetAudioWriter()

	// 		err := tones.Play(rctx, aw, ringVolume, tones.ETSIRinging)
	// 		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
	// 			c.log.Infow("cannot play dial tone", "error", err)
	// 		}
	// 	}()
	// }

	err = c.cc.transferCall(ctx, transferTo, headers, c.closing.Watch())
	if err != nil {
		c.log.Infow("outbound call failed to transfer", "error", err, "transferTo", transferTo)
		return err
	}

	c.log.Infow("outbound call transferred", "transferTo", transferTo)

	// Give time for the peer to hang up first, but hang up ourselves if this doesn't happen within 1 second
	time.AfterFunc(referByeTimeout, func() {
		c.CloseWithReason(ctx, CallHangup, stats.Success("call transferred"), livekit.DisconnectReason_CLIENT_INITIATED)
	})

	return nil
}

func (c *Client) newOutbound(log logger.Logger, id LocalTag, from, contact URI, displayName *string, getHeaders setHeadersFunc) *sipOutbound {
	from = from.Normalize()
	if displayName == nil { // Nothing specified, preserve legacy behavior
		displayName = &from.User
	}

	fromHeader := &sip.FromHeader{
		DisplayName: *displayName,
		Address:     *from.GetURI(),
		Params:      sip.NewParams(),
	}
	contactHeader := &sip.ContactHeader{
		Address: *contact.GetContactURI(),
	}
	fromHeader.Params.Add("tag", string(id))
	return &sipOutbound{
		log:         log,
		c:           c,
		id:          id,
		callID:      guid.HashedID(string(id)),
		from:        fromHeader,
		contact:     contactHeader,
		referDone:   make(chan error), // Do not buffer the channel to avoid reading a result for an old request
		nextCSeq:    1,
		getHeaders:  getHeaders,
		callTimeout: time.NewTimer(callTimeout),
	}
}

type sipOutbound struct {
	log          logger.Logger
	c            *Client
	id           LocalTag
	from         *sip.FromHeader
	contact      *sip.ContactHeader
	routeHeaders []string

	mu         sync.RWMutex
	tag        RemoteTag
	callID     string
	invite     *sip.Request
	inviteOk   *sip.Response
	localSDP   []byte // SDP Offer, constrained by the answer
	to         *sip.ToHeader
	nextCSeq   uint32
	getHeaders setHeadersFunc

	referCseq        uint32
	referDone        chan error
	latestInviteCSeq uint32

	// Session timer (RFC 4028), UAC side. Negotiated from the INVITE 2xx
	sessionExpires uint32
	minSe          uint32
	refresher      string
	refreshTimer   *time.Timer
	timerCancel    context.CancelFunc

	callTimeout *time.Timer
}

func (c *sipOutbound) From() sip.Uri {
	return c.from.Address
}

func (c *sipOutbound) To() sip.Uri {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.to == nil {
		return sip.Uri{}
	}
	return c.to.Address
}

func (c *sipOutbound) Address() sip.Uri {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.invite == nil {
		return sip.Uri{}
	}
	return c.invite.Recipient
}

func (c *sipOutbound) ID() LocalTag {
	return c.id
}

func (c *sipOutbound) Tag() RemoteTag {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tag
}

func (c *sipOutbound) SIPCallID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.callID
}

func (c *sipOutbound) InviteCSeq() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latestInviteCSeq
}

func (c *sipOutbound) RecordInvite(cseq uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cseq > c.latestInviteCSeq {
		c.latestInviteCSeq = cseq
	}
}

// SetLocalSDP stores the precomputed local SDP for re-INVITE (from ApplyWithLocal).
func (c *sipOutbound) SetLocalSDP(localSDP []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localSDP = localSDP
}

// LocalSDP returns the precomputed local SDP for re-INVITE (from ApplyWithLocal).
func (c *sipOutbound) LocalSDP() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.localSDP
}

// Returns the original SDP offer.
func (c *sipOutbound) OwnSDP() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.invite == nil {
		return nil
	}
	body := c.invite.Body()
	if len(body) == 0 {
		return nil
	}
	out := make([]byte, len(body))
	copy(out, body)
	return out
}

func (c *sipOutbound) RemoteHeaders() Headers {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.inviteOk == nil {
		return nil
	}
	return c.inviteOk.Headers()
}

func (c *sipOutbound) Invite(ctx context.Context, to URI, user, pass string, headers map[string]string, sdpOffer []byte, setState sipRespFunc) ([]byte, error) {
	ctx, span := Tracer.Start(ctx, "sip.outbound.Invite")
	defer span.End()
	c.mu.Lock()
	defer c.mu.Unlock()
	toHeader := &sip.ToHeader{Address: *to.GetURI()}

	var (
		sipHeaders         Headers
		authHeader         = ""
		authHeaderRespName string
		req                *sip.Request
		resp               *sip.Response
		err                error
	)
	if keys := maps.Keys(headers); len(keys) != 0 {
		sort.Strings(keys)
		for _, key := range keys {
			sipHeaders = append(sipHeaders, sip.NewHeader(key, headers[key]))
		}
	}
authLoop:
	for try := 0; ; try++ {
		if try >= 5 {
			return nil, psrpc.NewError(psrpc.FailedPrecondition, ErrAuthMaxRetry)
		}
		req, resp, err = c.attemptInvite(ctx, sip.CallIDHeader(c.callID), toHeader, sdpOffer, authHeaderRespName, authHeader, sipHeaders, setState)
		if err != nil {
			return nil, err
		}
		var authHeaderName string
		switch resp.StatusCode {
		case sip.StatusOK:
			break authLoop
		default:
			return nil, fmt.Errorf("unexpected status from INVITE response: %w", &livekit.SIPStatus{
				Code:   livekit.SIPStatusCode(resp.StatusCode),
				Status: resp.Reason,
			})
		case sip.StatusBadRequest,
			sip.StatusNotFound,
			sip.StatusTemporarilyUnavailable,
			sip.StatusNotAcceptableHere,
			sip.StatusBusyHere:
			err := &livekit.SIPStatus{
				Code:   livekit.SIPStatusCode(resp.StatusCode),
				Status: resp.Reason,
			}
			if body := resp.Body(); len(body) != 0 {
				err.Status = string(body)
			} else if s := resp.GetHeader("X-Twilio-Error"); s != nil {
				err.Status = s.Value()
			}
			return nil, fmt.Errorf("INVITE failed: %w", err)
		case sip.StatusUnauthorized:
			authHeaderName = "WWW-Authenticate"
			authHeaderRespName = "Authorization"
		case sip.StatusProxyAuthRequired:
			authHeaderName = "Proxy-Authenticate"
			authHeaderRespName = "Proxy-Authorization"
		}
		c.log.Infow("auth requested", "status", resp.StatusCode, "body", string(resp.Body()))
		// auth required
		if user == "" || pass == "" {
			return nil, psrpc.NewError(psrpc.FailedPrecondition, ErrAuthMissingCreds)
		}
		headerVal := resp.GetHeader(authHeaderName)
		if headerVal == nil {
			return nil, psrpc.NewError(psrpc.FailedPrecondition, ErrAuthNoHeader)
		}
		challengeStr := headerVal.Value()
		challenge, err := digest.ParseChallenge(challengeStr)
		if err != nil {
			return nil, psrpc.NewErrorf(psrpc.Internal, "invalid challenge %q: %v", challengeStr, err)
		}
		toHeader := resp.To()
		if toHeader == nil {
			return nil, psrpc.NewErrorf(psrpc.Internal, "no 'To' header on Response")
		}

		cred, err := digest.Digest(challenge, digest.Options{
			Method:   req.Method.String(),
			URI:      toHeader.Address.String(),
			Username: user,
			Password: pass,
		})
		if err != nil {
			return nil, err
		}
		authHeader = cred.String()
		// Try again with a computed digest
	}

	c.invite, c.inviteOk = req, resp
	toHeader = resp.To()
	if toHeader == nil {
		return nil, psrpc.NewErrorf(psrpc.Internal, "no To header in INVITE response")
	}
	var ok bool
	c.tag, ok = getTagFrom(toHeader.Params)
	if !ok {
		return nil, psrpc.NewErrorf(psrpc.Internal, "no tag in To header in INVITE response")
	}

	if cont := resp.Contact(); cont != nil {
		req.Recipient = cont.Address
		if req.Recipient.Port == 0 {
			req.Recipient.Port = 5060
		}
	}

	// We currently don't plumb the request back to caller to construct the ACK with.
	// Thus, we need to modify the request to update any route sets.
	for req.RemoveHeader("Route") {
	}
	for _, hdr := range resp.GetHeaders("Record-Route") {
		req.PrependHeader(&sip.RouteHeader{Address: hdr.(*sip.RecordRouteHeader).Address})
	}

	c.setupSessionTimer()

	return c.inviteOk.Body(), nil
}

func (c *sipOutbound) AcceptBye(req *sip.Request, tx sip.ServerTransaction) {
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drop() // mark as closed
}

func (c *sipOutbound) AckInviteOK(ctx context.Context) error {
	ctx, span := Tracer.Start(ctx, "sip.outbound.AckInviteOK")
	defer span.End()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.invite == nil || c.inviteOk == nil {
		return psrpc.NewErrorf(psrpc.Canceled, "call already closed")
	}
	return c.c.sipCli.WriteRequest(sip.NewAckRequest(c.invite, c.inviteOk, nil))
}

func (c *sipOutbound) attemptInvite(ctx context.Context, callID sip.CallIDHeader, to *sip.ToHeader, offer []byte, authHeaderName, authHeader string, headers Headers, setState sipRespFunc) (*sip.Request, *sip.Response, error) {
	ctx, span := Tracer.Start(ctx, "sip.outbound.attemptInvite")
	defer span.End()
	req := sip.NewRequest(sip.INVITE, to.Address)
	c.setCSeq(req)
	req.RemoveHeader("Call-ID")
	req.AppendHeader(&callID)

	req.SetBody(offer)
	req.AppendHeader(to)
	req.AppendHeader(c.from)
	req.AppendHeader(c.contact)

	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.AppendHeader(sip.NewHeader("Allow", "INVITE, ACK, CANCEL, BYE, UPDATE, NOTIFY, REFER, MESSAGE, OPTIONS, INFO, SUBSCRIBE"))

	c.addSessionTimerRequestHeaders(req)

	if authHeader != "" {
		req.AppendHeader(sip.NewHeader(authHeaderName, authHeader))
	}
	for _, h := range headers {
		req.AppendHeader(h)
	}

	for _, route := range c.routeHeaders {
		req.PrependHeader(sip.NewHeader("Route", route))
	}

	tx, err := c.c.sipCli.TransactionRequest(req)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Terminate()

	// Log the actual local port used for TCP connections from the DialPort range
	if req.Transport() == "TCP" {
		// Type-assert to *sipgo.Client to access the embedded UserAgent
		if sipClient, ok := c.c.sipCli.(*sipgo.Client); ok {
			if tpl := sipClient.TransportLayer(); tpl != nil {
				// Try to get the connection using the destination address
				// The connection should be available after TransactionRequest creates it
				if dest := req.Destination(); dest != "" {
					if conn, err := tpl.GetConnection("tcp", dest); err == nil && conn != nil {
						if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok && tcpAddr != nil {
							c.log.Debugw("TCP connection using port on cloud-sip side", "port", tcpAddr.Port)
						}
					}
				}
			}
		}
	}

	resp, err := sipResponse(ctx, tx, c.c.closing.Watch(), setState)
	return req, resp, err
}

func (c *sipOutbound) WriteRequest(req *sip.Request) error {
	return c.c.sipCli.WriteRequest(req)
}

func (c *sipOutbound) Transaction(req *sip.Request) (sip.ClientTransaction, error) {
	return c.c.sipCli.TransactionRequest(req)
}

func (c *sipOutbound) setCSeq(req *sip.Request) {
	setCSeq(req, c.nextCSeq)

	c.nextCSeq++
}

func (c *sipOutbound) ResetRtpTimeout() {
	c.callTimeout.Reset(callTimeout)
}

func (c *sipOutbound) fillHeaders(headers map[string]string) map[string]string {
	if c == nil {
		return headers
	}
	call, ok := c.c.activeCalls[c.ID()]
	if !ok {
		return headers
	}
	call.setAttrsToHeaders(headers)
	return headers
}

func (c *sipOutbound) generateViaHeader(req *sip.Request) *sip.ViaHeader {
	newvia := &sip.ViaHeader{
		ProtocolName:    "SIP",
		ProtocolVersion: "2.0",
		Transport:       req.Transport(),
		Host:            c.c.sconf.SignalingIP.String(), // This can be rewritten by transport layer
		Port:            c.c.conf.SIPPort,               // This can be rewritten by transport layer
		Params:          sip.NewParams(),
	}
	// NOTE: Consider length of branch configurable
	newvia.Params.Add("branch", sip.GenerateBranchN(16))

	return newvia
}

func (c *sipOutbound) swapSrcDst(req *sip.Request) {
	dest := c.inviteOk.Destination()
	if contact := c.invite.Contact(); contact != nil {
		req.Recipient = contact.Address
		dest = ConvertURI(&contact.Address).GetDest()
	} else {
		req.Recipient = c.from.Address
	}
	if route := c.invite.RecordRoute(); route != nil {
		dest = ConvertURI(&route.Address).GetDest()
	}
	req.SetSource(c.inviteOk.Source())
	req.SetDestination(dest)
	req.RemoveHeader("From")
	req.AppendHeader((*sip.FromHeader)(c.to))
	req.RemoveHeader("To")
	req.AppendHeader((*sip.ToHeader)(c.from))
	// Remove all Via headers
	for req.RemoveHeader("Via") {
	}
	req.PrependHeader(c.generateViaHeader(req))

	rrHdrs := req.GetHeaders("Record-Route")
	for _, hdr := range rrHdrs {
		req.PrependHeader(&sip.RouteHeader{Address: hdr.(*sip.RecordRouteHeader).Address})
	}
	// Remove all Record-Route headers
	for req.RemoveHeader("Record-Route") {
	}
}

func (c *sipOutbound) peerSupportsTimer() bool {
	if c.inviteOk == nil {
		return false
	}
	for _, name := range []string{"Supported", "Require"} {
		for _, h := range c.inviteOk.GetHeaders(name) {
			for _, tag := range strings.Split(h.Value(), ",") {
				if strings.TrimSpace(tag) == "timer" {
					return true
				}
			}
		}
	}
	return false
}

func (c *sipOutbound) refresherForDialog() string {
	if c.inviteOk != nil {
		if h := c.inviteOk.GetHeader("Session-Expires"); h != nil {
			v := h.Value()
			if strings.Contains(v, "refresher=uas") {
				return "uas"
			}
			if strings.Contains(v, "refresher=uac") {
				return "uac"
			}
		}
	}
	return "uac"
}

// setupSessionTimer negotiates the session timer from the INVITE 2xx response.
// Must be called with c.mu held. No-op if the peer did not engage timers.
func (c *sipOutbound) setupSessionTimer() {
	hasSE := c.inviteOk != nil && c.inviteOk.GetHeader("Session-Expires") != nil
	if !hasSE && !c.peerSupportsTimer() && c.refreshTimer == nil {
		return
	}
	c.refresher = c.refresherForDialog()

	if c.refreshTimer == nil {
		c.minSe = minSESecs
		c.sessionExpires = sessionTimerSecs
	}
	if h := c.inviteOk.GetHeader("Min-SE"); h != nil {
		if secs, err := strconv.Atoi(strings.SplitN(h.Value(), ";", 2)[0]); err == nil && secs > 0 {
			c.minSe = max(c.minSe, uint32(secs))
		}
	}
	if h := c.inviteOk.GetHeader("Session-Expires"); h != nil {
		if v, err := strconv.Atoi(strings.SplitN(h.Value(), ";", 2)[0]); err == nil && v > 0 {
			c.sessionExpires = max(uint32(v), c.minSe)
		}
	}

	d := time.Duration(c.sessionExpires) * time.Second
	if c.refreshTimer == nil {
		c.refreshTimer = time.NewTimer(d)
	} else {
		c.refreshTimer.Reset(d)
	}
	c.log.Infow("session timer setup", "sessionExpires", c.sessionExpires, "minSe", c.minSe, "refresher", c.refresher)
}

func (c *sipOutbound) addSessionTimerRequestHeaders(req *sip.Request) {
	if req.GetHeader("Supported") == nil {
		req.AppendHeader(sip.NewHeader("Supported", "timer"))
	}
	se, minSe, refresher := c.sessionExpires, c.minSe, c.refresher
	if se == 0 {
		se, minSe, refresher = sessionTimerSecs, minSESecs, "uac"
	}
	if req.GetHeader("Session-Expires") == nil {
		req.AppendHeader(sip.NewHeader("Session-Expires", fmt.Sprintf("%d;refresher=%s", se, refresher)))
	}
	if req.GetHeader("Min-SE") == nil {
		req.AppendHeader(sip.NewHeader("Min-SE", fmt.Sprintf("%d", minSe)))
	}
}

func (c *sipOutbound) addSessionTimerHeaders(r *sip.Response) {
	var method sip.RequestMethod
	if cseq := r.CSeq(); cseq != nil {
		method = cseq.MethodName
	}
	is2xx := r.StatusCode >= 200 && r.StatusCode < 300

	if is2xx && (method == sip.INVITE || method == sip.UPDATE || method == sip.OPTIONS) {
		r.AppendHeader(sip.NewHeader("Supported", "timer"))
	}
	if is2xx && (method == sip.INVITE || method == sip.UPDATE) && c.sessionExpires != 0 {
		r.AppendHeader(sip.NewHeader("Session-Expires", fmt.Sprintf("%d;refresher=%s", c.sessionExpires, c.refresher)))
	}
	if r.StatusCode == 422 && c.sessionExpires != 0 {
		r.AppendHeader(sip.NewHeader("Min-SE", fmt.Sprintf("%d", c.minSe)))
	}
}

func (c *sipOutbound) AcceptUpdate(req *sip.Request, tx sip.ServerTransaction) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(req.Body()) == 0 {
		c.handleTimerRefresh(req, tx)
		return
	}
	r := sip.NewResponseFromRequest(req, sip.StatusNotImplemented, "Not Implemented", nil)
	c.addSessionTimerHeaders(r)
	if err := tx.Respond(r); err != nil {
		c.log.Errorw("failed to send 501 for UPDATE with SDP", err)
	}
}

func (c *sipOutbound) AcceptReInvite(req *sip.Request, tx sip.ServerTransaction, medias *MediaOrchestrator) {
	var newCSeq uint32
	if cseq := req.CSeq(); cseq != nil {
		newCSeq = cseq.SeqNo
	}
	offer := req.Body()

	var answer []byte
	if medias != nil && len(offer) > 0 {
		a, err := medias.AnswerSDP(offer)
		if err != nil {
			c.log.Errorw("failed to renegotiate re-INVITE, echoing current SDP", err)
		} else {
			answer = a
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(answer) > 0 {
		c.localSDP = answer
	}
	respSDP := c.localSDP
	if len(respSDP) == 0 {
		c.log.Warnw("re-INVITE received but no SDP to respond with", nil)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "", nil))
		return
	}
	if newCSeq > c.latestInviteCSeq {
		c.latestInviteCSeq = newCSeq
	}
	if c.refreshTimer != nil {
		c.refreshTimer.Reset(time.Duration(c.sessionExpires) * time.Second)
	}

	r := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", respSDP)
	r.AppendHeader(&contentTypeHeaderSDP)
	r.AppendHeader(c.contact)
	c.addSessionTimerHeaders(r)
	if err := tx.Respond(r); err != nil {
		c.log.Errorw("failed to respond to re-INVITE", err)
	}
}

// local expiry timer. Must be called with c.mu held.
func (c *sipOutbound) handleTimerRefresh(req *sip.Request, tx sip.ServerTransaction) {
	if c.sessionExpires == 0 || c.refreshTimer == nil {
		r := sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Session Timer Not Negotiated", nil)
		c.addSessionTimerHeaders(r)
		if err := tx.Respond(r); err != nil {
			c.log.Errorw("failed to send 400 for session refresh", err)
		}
		return
	}

	c.refreshTimer.Stop()
	r := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	c.addSessionTimerHeaders(r)
	if err := tx.Respond(r); err != nil {
		c.log.Errorw("failed to send 200 OK for session refresh", err)
	}
	c.refreshTimer.Reset(time.Duration(c.sessionExpires) * time.Second)
}

func (c *sipOutbound) newInDialogRequest(method sip.RequestMethod, body []byte) *sip.Request {
	req := sip.NewRequest(method, c.invite.Recipient)
	req.SipVersion = c.invite.SipVersion

	sip.CopyHeaders("Via", c.invite, req)
	req.Via().Params.Add("branch", sip.GenerateBranch())

	if len(c.invite.GetHeaders("Route")) > 0 {
		sip.CopyHeaders("Route", c.invite, req)
	} else {
		hdrs := c.inviteOk.GetHeaders("Record-Route")
		for i := len(hdrs) - 1; i >= 0; i-- {
			req.AppendHeader(sip.HeaderClone(hdrs[i]))
		}
	}

	maxFwd := sip.MaxForwardsHeader(70)
	req.AppendHeader(&maxFwd)
	if h := c.invite.From(); h != nil {
		req.AppendHeader(sip.HeaderClone(h))
	}
	if h := c.inviteOk.To(); h != nil {
		req.AppendHeader(sip.HeaderClone(h))
	}
	if h := c.invite.CallID(); h != nil {
		req.AppendHeader(sip.HeaderClone(h))
	}
	req.AppendHeader(c.contact)

	c.setCSeq(req)

	req.SetBody(body)
	req.SetTransport(c.invite.Transport())
	req.SetSource(c.invite.Source())
	req.SetDestination(c.invite.Destination())
	return req
}

func (c *sipOutbound) sendUpdate(ctx context.Context) {
	c.mu.Lock()
	if c.invite == nil || c.inviteOk == nil {
		c.mu.Unlock()
		return
	}

	req := c.newInDialogRequest(sip.UPDATE, nil)
	c.addSessionTimerRequestHeaders(req)
	for k, v := range c.fillHeaders(nil) {
		req.AppendHeader(sip.NewHeader(k, v))
	}
	c.mu.Unlock()

	tx, err := c.Transaction(req)
	if err != nil {
		c.log.Errorw("failed to create UPDATE transaction", err)
		return
	}
	defer tx.Terminate()

	ctx, cancel := context.WithTimeout(ctx, 32*time.Second)
	defer cancel()

	resp, err := sipResponse(ctx, tx, c.c.closing.Watch(), nil)
	if err != nil {
		c.log.Errorw("session refresh UPDATE failed", err)
		return
	}

	if resp.StatusCode == 422 { // Session Interval Too Small (RFC 4028)
		c.mu.Lock()
		bumped := false
		if h := resp.GetHeader("Min-SE"); h != nil {
			if v, err := strconv.Atoi(strings.SplitN(h.Value(), ";", 2)[0]); err == nil && uint32(v) > c.sessionExpires {
				c.sessionExpires = uint32(v)
				bumped = true
			}
		}
		c.mu.Unlock()
		if bumped {
			c.sendUpdate(ctx)
		}
		return
	}

	c.mu.Lock()
	if c.refreshTimer != nil {
		c.refreshTimer.Reset(time.Duration(c.sessionExpires) * time.Second)
	}
	c.mu.Unlock()
}

func (c *sipOutbound) accepted(ctx context.Context) {
	c.mu.Lock()
	if c.refreshTimer == nil {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.timerCancel = cancel
	refreshTimer := c.refreshTimer
	refresher := c.refresher
	c.mu.Unlock()

	// Expiry watchdog: if the session is not refreshed in time, tear the call down.
	go func() {
		select {
		case <-refreshTimer.C:
			c.callTimeout.Reset(callTimeout)
			// c.log.Warnw("session refresh timer expired, closing call", nil)
			// if oc := c.c.getActiveCall(c.id); oc != nil {
			// 	oc.CloseWithReason(ctx, callDropped, stats.ClientError("session-timer-expired"), livekit.DisconnectReason_CLIENT_INITIATED)
			// } else {
			// 	c.Close(ctx)
			// }
		case <-ctx.Done():
		}
	}()

	// When we are the refresher, periodically send UPDATE at half the interval.
	if refresher == "uac" {
		go func() {
			for {
				c.mu.RLock()
				interval := time.Duration(max(c.sessionExpires/2, c.minSe)) * time.Second
				c.mu.RUnlock()
				select {
				case <-time.After(interval):
					c.sendUpdate(ctx)
				case <-ctx.Done():
					return
				}
			}
		}()
	}
}

// SendReInvite implements [CallHandler].
func (c *sipOutbound) SendReInvite(ctx context.Context, offerSDP []byte) (*sip.Response, error) {
	c.mu.Lock()
	if c.invite == nil || c.inviteOk == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("call not established")
	}

	req := c.newInDialogRequest(sip.INVITE, offerSDP)
	req.AppendHeader(&contentTypeHeaderSDP)

	// Re-INVITEs refresh the session timer (RFC 4028); carry the headers.
	c.addSessionTimerRequestHeaders(req)

	// Apply dynamic attribute->header mapping via fillHeaders, the same mechanism used by
	// sendBye/sendStatus.
	for k, v := range c.fillHeaders(nil) {
		req.AppendHeader(sip.NewHeader(k, v))
	}
	c.mu.Unlock()

	tx, err := c.Transaction(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create re-INVITE transaction: %w", err)
	}
	defer tx.Terminate()

	ctx, cancel := context.WithTimeout(ctx, 32*time.Second)
	defer cancel()

	resp, err := sipResponse(ctx, tx, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("re-INVITE failed: %w", err)
	}

	if resp.StatusCode == 200 {
		_ = c.WriteRequest(sip.NewAckRequest(req, resp, nil))
	}

	return resp, nil
}

func (c *sipOutbound) sendBye(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	if c.invite == nil || c.inviteOk == nil {
		return // call wasn't established
	}
	ctx, span := Tracer.Start(ctx, "sip.outbound.sendBye")
	defer span.End()
	r := sip.NewByeRequest(c.invite, c.inviteOk, nil)
	r.AppendHeader(sip.NewHeader("User-Agent", "LiveKit"))
	if c.getHeaders != nil {
		for k, v := range c.getHeaders(nil) {
			r.AppendHeader(sip.NewHeader(k, v))
		}
	}
	if c.c.closing.IsBroken() {
		// do not wait for a response
		_ = c.WriteRequest(r)
		return
	}
	c.setCSeq(r)
	c.drop()
	sendAndACK(ctx, c, r)
}

func (c *sipOutbound) sendCancel(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	if c.invite == nil {
		return
	}
	ctx, span := Tracer.Start(ctx, "sip.outbound.sendCancel")
	defer span.End()
	r := sip.NewCancelRequest(c.invite)
	r.AppendHeader(sip.NewHeader("User-Agent", "LiveKit"))
	if c.getHeaders != nil {
		for k, v := range c.getHeaders(nil) {
			r.AppendHeader(sip.NewHeader(k, v))
		}
	}
	_ = c.WriteRequest(r)
	c.drop()
}

func (c *sipOutbound) drop() {
	if c.timerCancel != nil {
		c.timerCancel()
		c.timerCancel = nil
	}
	if c.refreshTimer != nil {
		c.refreshTimer.Stop()
		c.refreshTimer = nil
	}
	c.sessionExpires = 0
	c.invite = nil
	c.inviteOk = nil
	c.nextCSeq = 0
}

func (c *sipOutbound) Drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drop()
}

func (c *sipOutbound) transferCall(ctx context.Context, transferTo string, headers map[string]string, callDone <-chan struct{}) error {
	c.mu.Lock()

	if c.invite == nil || c.inviteOk == nil {
		c.mu.Unlock()
		return psrpc.NewErrorf(psrpc.FailedPrecondition, "can't transfer non established call") // call wasn't established
	}

	if c.c.closing.IsBroken() {
		c.mu.Unlock()
		return psrpc.NewErrorf(psrpc.FailedPrecondition, "can't transfer hung up call")
	}

	if c.getHeaders != nil {
		headers = c.getHeaders(headers)
	}

	req := NewReferRequest(c.invite, c.inviteOk, c.contact, transferTo, headers)
	c.setCSeq(req)
	cseq := req.CSeq()

	if cseq == nil {
		c.mu.Unlock()
		return psrpc.NewErrorf(psrpc.Internal, "missing CSeq header in REFER request")
	}
	c.referCseq = cseq.SeqNo
	c.mu.Unlock()

	_, err := sendRefer(ctx, c, req, c.c.closing.Watch())
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return psrpc.NewErrorf(psrpc.Canceled, "refer canceled")
	case <-callDone:
		// At this point, REFER was accepted, but we received a BYE, nothing to do, also not an error
		c.log.Infow("refer canceled by BYE from remote")
		return nil
	case err := <-c.referDone:
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *sipOutbound) handleNotify(req *sip.Request, tx sip.ServerTransaction) error {
	method, cseq, status, reason, err := handleNotify(req)
	if err != nil {
		c.log.Infow("error parsing NOTIFY request", "error", err)

		return err
	}

	c.log.Infow("handling NOTIFY", "method", method, "status", status, "reason", reason, "cseq", cseq)

	switch method {
	default:
		return nil
	case sip.REFER:
		c.mu.RLock()
		defer c.mu.RUnlock()
		handleReferNotify(cseq, status, reason, c.referCseq, c.referDone)
		return nil
	}
}

func (c *sipOutbound) Close(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inviteOk != nil {
		c.sendBye(ctx)
	} else if c.invite != nil {
		c.sendCancel(ctx)
	} else {
		c.drop()
	}
}
