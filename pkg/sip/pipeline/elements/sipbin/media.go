package sipbin

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/gstsdp"
	"github.com/go-gst/go-gst/gst/rtp"
	"github.com/livekit/protocol/livekit"
)

func (e *SipBin) extractMediaCases(self *gst.Bin, media *gstsdp.Media, kind livekit.TrackSource) {
	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Extracting media cases for media %s with %d formats", media.GetMedia(), media.FormatsLen()))
	for i := 0; ; i++ {
		format := media.GetAttributeValN("rtpmap", i)
		if format == "" {
			self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Finished extracting media cases for media %s after %d formats", media.GetMedia(), i))
			break
		}
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Processing rtpmap format: %s", format))

		ptStr, rest, ok := strings.Cut(format, " ")
		if !ok {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid rtpmap format: %s", format))
			continue
		}
		pt, err := strconv.Atoi(ptStr)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid payload type %s in rtpmap attribute: %v", ptStr, err))
			continue
		}
		if pt < 0 || pt > 127 {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Payload type out of range in rtpmap attribute: %d", pt))
			continue
		}
		if rest == "" {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Missing encoding in rtpmap attribute: %s", format))
			continue
		}
		encoding, _, ok := strings.Cut(rest, "/")
		if !ok {
			encoding = rest
		}
		if encoding == "" {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Empty encoding in rtpmap attribute: %s", format))
			continue
		}

		if encoding == strings.ToLower(encoding) {
			if encCase, exist := e.encodingCase[kind][uint8(pt)]; exist && encCase != EncodingCaseLower {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Conflicting encoding case for payload type %d: already have %v, new encoding %s", pt, e.encodingCase[kind][uint8(pt)], encoding))
				continue
			}
			e.encodingCase[kind][uint8(pt)] = EncodingCaseLower
		} else if encoding == strings.ToUpper(encoding) {
			if encCase, exist := e.encodingCase[kind][uint8(pt)]; exist && encCase != EncodingCaseUpper {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Conflicting encoding case for payload type %d: already have %v, new encoding %s", pt, e.encodingCase[kind][uint8(pt)], encoding))
				continue
			}
			e.encodingCase[kind][uint8(pt)] = EncodingCaseUpper
		}

		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Extracted encoding case for payload type %d: %s -> %v", pt, encoding, e.encodingCase[kind][uint8(pt)]))
	}
}

func (e *SipBin) normalizeEncodingName(self *gst.Bin, kind livekit.TrackSource, caps *gst.Caps) *gst.Caps {
	res := caps.Copy()

	for i := range res.GetSize() {
		structure := res.GetStructureAt(i)
		encoding, err := structure.GetString("encoding-name")
		if err != nil || encoding == "" {
			continue
		}

		payload, err := structure.GetInt("payload")
		if err != nil || payload < 0 || payload > 127 {
			continue
		}
		encCase, exist := e.encodingCase[kind][uint8(payload)]
		if !exist {
			continue
		}
		switch encCase {
		case EncodingCaseLower:
			encoding = strings.ToLower(encoding)
		case EncodingCaseUpper:
			encoding = strings.ToUpper(encoding)
		}
		if err := structure.SetString("encoding-name", encoding); err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set encoding-name value on caps structure: %v", err))
			continue
		}
		self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Normalized encoding name for payload type %d: %s", payload, structure.String()))
	}
	return res
}

func (e *SipBin) makeOfferMedia(self *gst.Bin, kind livekit.TrackSource, idx int, proto string) (*gstsdp.Media, error) {
	var targetMedia string
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		targetMedia = "video"
	case livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		targetMedia = "audio"
	default:
		return nil, fmt.Errorf("unsupported track source: %d", kind)
	}

	media, err := gstsdp.NewMedia()
	if err != nil {
		return nil, fmt.Errorf("failed to create SDP media: %w", err)
	}

	targetCaps := make([]*gst.Caps, 0, len(e.formats))
	for _, caps := range e.formats {
		for i := range caps.GetSize() {
			structure := caps.GetStructureAt(i)
			media, err := structure.GetString("media")
			if err != nil || media == "" {
				continue
			}
			if media == targetMedia {
				targetCaps = append(targetCaps, caps.Copy())
				break
			}
		}
	}

	offerCaps := make([]*gst.Caps, 0, len(targetCaps))
	dynamicPt := uint8(96)
	for _, caps := range targetCaps {
		for i := range caps.GetSize() {
			structure := caps.GetStructureAt(i)
			encoding, err := structure.GetString("encoding-name")
			if err != nil || encoding == "" {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get encoding-name value from caps structure: %v", err))
				continue
			}
			media, err := structure.GetString("media")
			if err != nil || media == "" {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get media value from caps structure: %v", err))
				continue
			}

			info := rtp.PayloadInfoForName(media, encoding)
			if info == nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("No payload info found for media %s and encoding %s", media, encoding))
				continue
			}
			pt := info.PayloadType()
			if pt >= 96 {
				pt = dynamicPt
				dynamicPt++
			}
			if err := structure.SetInt("payload", int(pt)); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set payload type on caps structure: %v", err))
				continue
			}
			offerCaps = append(offerCaps, caps.Copy().Fixate())
		}
	}

	if len(offerCaps) == 0 {
		return nil, fmt.Errorf("no offer caps found for track source %d", kind)
	}

	for i := range offerCaps {
		offerCaps[i] = e.normalizeEncodingName(self, kind, offerCaps[i])
	}

	// Map each offered payload type to its caps for the receive path.
	for _, caps := range offerCaps {
		for i := range caps.GetSize() {
			pt, err := caps.GetStructureAt(i).GetInt("payload")
			if err != nil || pt < 0 || pt > 127 {
				continue
			}
			e.PtMap[kind][uint8(pt)] = caps.Copy()
		}
	}

	if ret := gstsdp.MediaSetFromCaps(offerCaps[0], media); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set media from caps: %v", ret)
	}
	for i := range offerCaps[0].GetSize() - 1 {
		structure := offerCaps[0].GetStructureAt(i + 1)
		if ret := gstsdp.MediaAddMediaFromStructure(structure, media); ret != gstsdp.SDPResultOk {
			return nil, fmt.Errorf("failed to add media from structure: %v", ret)
		}
	}

	offerCaps = offerCaps[1:]
	for _, caps := range offerCaps {
		for i := range caps.GetSize() {
			structure := caps.GetStructureAt(i)
			if ret := gstsdp.MediaAddMediaFromStructure(structure, media); ret != gstsdp.SDPResultOk {
				return nil, fmt.Errorf("failed to add media from structure: %v", ret)
			}
		}
	}

	if proto == "" {
		proto = "RTP/AVP"
	}
	if ret := media.SetProto(proto); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set proto on media: %v", ret)
	}

	if ret := media.AddAttribute("rtcp-fb", "* nack pli"); ret != gstsdp.SDPResultOk {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
	}
	if ret := media.AddAttribute("rtcp-fb", "* ccm fir"); ret != gstsdp.SDPResultOk {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
	}

	switch kind {
	case livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		if ret := media.AddAttribute("content", "slides"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	case livekit.TrackSource_CAMERA:
		if ret := media.AddAttribute("content", "main"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	}

	track, err := e.NewTrack(self, idx, kind, proto)
	if err != nil {
		return nil, fmt.Errorf("failed to create track for media %d: %w", idx, err)
	}
	e.Tracks[kind] = track

	port := uint(track.rtpConn.LocalAddr().(*net.UDPAddr).Port)
	if ret := media.SetPortInfo(port, 1); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set port info on media: %v", ret)
	}

	return media, nil
}

var bareFmtpName = regexp.MustCompile(`^\d+(?:[,-]\d+)*$`)

func mediaCapsFixBareFmtp(caps *gst.Caps) *gst.Caps {
	for i := range caps.GetSize() {
		structure := caps.GetStructureAt(i)
		toRemove := make([]string, 0)
		for key, value := range structure.Values() {
			str, ok := value.(string)
			if !ok || str != "1" {
				continue
			}
			if bareFmtpName.MatchString(key) {
				toRemove = append(toRemove, key)
			}
		}
		for _, key := range toRemove {
			structure.RemoveValue(key)
		}
	}
	return caps
}

func kindToMediaType(kind livekit.TrackSource) string {
	switch kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		return "video"
	case livekit.TrackSource_MICROPHONE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		return "audio"
	default:
		return "unknown"
	}
}

func mediaCapsRtcpFeedback(self *gst.Bin, caps *gst.Caps) *gst.Caps {
	for i := range caps.GetSize() {
		structure := caps.GetStructureAt(i)
		if !structure.HasField("rtcp-fb-nack-pli") {
			if err := structure.SetBool("rtcp-fb-nack-pli", true); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set rtcp-fb-nack-pli attribute on caps structure: %v", err))
			}
		}
		if !structure.HasField("rtcp-fb-ccm-fir") {
			if err := structure.SetBool("rtcp-fb-ccm-fir", true); err != nil {
				self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set rtcp-fb-ccm-fir attribute on caps structure: %v", err))
			}
		}
	}
	return caps
}

func (e *SipBin) selectCapsForMedia(self *gst.Bin, media *gstsdp.Media, kind livekit.TrackSource) (*gst.Caps, error) {
	mediaCaps := make([]*gst.Caps, 0, media.FormatsLen())
	for _, format := range media.Formats() {
		pt, err := strconv.Atoi(format)
		if err != nil {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Invalid format %s for media %s: %v", format, media.GetMedia(), err))
			continue
		}
		caps, err := media.GetCaps(pt)
		if err != nil || caps.GetSize() == 0 {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to get caps for format %s and payload type %d: %v", format, pt, err))
			continue
		}
		caps.GetStructureAt(0).SetName("application/x-rtp")
		// caps.GetStructureAt(0).RemoveValue("proto") // TODO: properly handle srtp if we want to support it

		switch kind {
		case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
			caps = mediaCapsRtcpFeedback(self, caps)
		}
		caps = mediaCapsFixBareFmtp(caps)

		info := rtp.PayloadInfoForPt(uint8(pt))
		if info != nil && info.PayloadType() < 96 {
			encodingName, err := caps.GetStructureAt(0).GetString("encoding-name")
			if err != nil || encodingName != info.EncodingName() {
				if err := caps.GetStructureAt(0).SetString("encoding-name", info.EncodingName()); err != nil {
					self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to set encoding-name attribute on caps: %v", err))
				}
			}
		}

		if existing, exist := e.PtMap[kind][uint8(pt)]; exist && existing != nil && !existing.IsEqual(caps) {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Received duplicate caps for payload type %d: existing %s, new %s", pt, existing.String(), caps.String()))
		}
		e.PtMap[kind][uint8(pt)] = caps

		mediaCaps = append(mediaCaps, caps)
	}

	if len(mediaCaps) == 0 {
		return nil, fmt.Errorf("no caps found for media %s", media.GetMedia())
	}

	res := gst.NewEmptyCaps()
	for _, formatCaps := range e.formats {
		leftover := make([]*gst.Caps, 0, len(mediaCaps))
		for _, caps := range mediaCaps {
			self.Log(CAT, gst.LevelTrace, fmt.Sprintf("Intersecting media caps %s with format caps %s", caps.String(), formatCaps.String()))
			icaps := caps.IntersectFull(formatCaps, gst.CapsIntersectFirst)
			if icaps != nil && !icaps.IsEmpty() {
				self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Found compatible caps for media %s: %s", kind.String(), icaps.String()))
				res.Append(caps.Copy())
			} else {
				leftover = append(leftover, caps)
			}
		}
		mediaCaps = leftover
	}

	if res.IsEmpty() {
		return nil, fmt.Errorf("no compatible caps found for media %s: %s", media.GetMedia(), media.AsText())
	}
	return res, nil
}

func (e *SipBin) makeTrackMedia(self *gst.Bin, track *SipTrack, caps *gst.Caps) (*gstsdp.Media, error) {
	if caps == nil {
		caps = track.Caps
	}
	if caps == nil {
		return nil, fmt.Errorf("no caps available for track media")
	}

	caps = e.normalizeEncodingName(self, track.Kind, caps)

	self.Log(CAT, gst.LevelDebug, fmt.Sprintf("Creating media for track %d with caps: %s", track.Idx, caps.String()))

	media, err := gstsdp.NewMedia()
	if err != nil {
		return nil, fmt.Errorf("failed to create SDP media: %w", err)
	}
	if ret := gstsdp.MediaSetFromCaps(caps, media); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set media from caps: %v", ret)
	}
	for i := range caps.GetSize() - 1 {
		structure := caps.GetStructureAt(i + 1)
		if ret := gstsdp.MediaAddMediaFromStructure(structure, media); ret != gstsdp.SDPResultOk {
			return nil, fmt.Errorf("failed to add media from structure: %v", ret)
		}
	}

	if ret := media.SetPortInfo(uint(track.rtpConn.LocalAddr().(*net.UDPAddr).Port), 1); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set port info on media: %v", ret)
	}
	if ret := media.SetProto(track.Proto); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to set proto on media: %v", ret)
	}

	switch track.Kind {
	case livekit.TrackSource_CAMERA, livekit.TrackSource_SCREEN_SHARE:
		if ret := media.AddAttribute("rtcp-fb", "* nack pli"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
		}
		if ret := media.AddAttribute("rtcp-fb", "* ccm fir"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add rtcp-fb attribute to media: %v", ret))
		}
	}

	if ret := media.AddAttribute("rtcp", strconv.Itoa(track.rtcpConn.LocalAddr().(*net.UDPAddr).Port)); ret != gstsdp.SDPResultOk {
		return nil, fmt.Errorf("failed to add rtcp attribute to media: %v", err)
	}

	switch track.Kind {
	case livekit.TrackSource_SCREEN_SHARE, livekit.TrackSource_SCREEN_SHARE_AUDIO:
		if ret := media.AddAttribute("content", "slides"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	case livekit.TrackSource_CAMERA:
		if ret := media.AddAttribute("content", "main"); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add content attribute to media: %v", ret))
		}
	}

	if !track.recv && !track.send {
		if ret := media.AddAttribute("inactive", ""); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add inactive attribute to media: %v", ret))
		}
	} else if track.recv && !track.send {
		if ret := media.AddAttribute("recvonly", ""); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add direction attribute to media: %v", ret))
		}
	} else if !track.recv && track.send {
		if ret := media.AddAttribute("sendonly", ""); ret != gstsdp.SDPResultOk {
			self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Failed to add direction attribute to media: %v", ret))
		}
	}

	return media, nil
}
