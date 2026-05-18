package h264rtppaybin

import (
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
)

type profileCase int

const (
	lowerCase profileCase = iota
	upperCase
)

type profile int

const (
	profileUnknown profile = iota
	profileConstrainedBaseline
	profileBaseline
	profileMain
	profileHigh
	profileConstrainedHigh
)

type profileLevelID struct {
	profileCase profileCase
	profile     profile
	profileIDC  uint8
	profileIOP  uint8
	levelIDC    uint8
	isLevel1b   bool
}

func parseProfileLevelID(s string) (profileLevelID, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 6 {
		return profileLevelID{}, fmt.Errorf("profile-level-id must be 6 hex chars, got %q", s)
	}

	var profileCase profileCase
	if s != strings.ToLower(s) {
		profileCase = upperCase
	} else {
		profileCase = lowerCase
	}

	b, err := hex.DecodeString(s)
	if err != nil {
		return profileLevelID{}, fmt.Errorf("invalid hex in profile-level-id %q: %w", s, err)
	}

	p := profileLevelID{
		profileCase: profileCase,
		profileIDC:  b[0],
		profileIOP:  b[1],
		levelIDC:    b[2],
	}
	p.profile = identifyProfile(p.profileIDC, p.profileIOP)
	p.isLevel1b = isLevel1b(p.profileIDC, p.profileIOP, p.levelIDC)
	return p, nil
}

func (p profileLevelID) String() string {
	if p.profileCase == upperCase {
		return fmt.Sprintf("%02X%02X%02X", p.profileIDC, p.profileIOP, p.levelIDC)
	}
	return fmt.Sprintf("%02x%02x%02x", p.profileIDC, p.profileIOP, p.levelIDC)
}

func identifyProfile(idc, iop uint8) profile {
	switch idc {
	case 0x42:
		if iop&0x40 != 0 {
			return profileConstrainedBaseline
		}
		return profileBaseline

	case 0x4D:
		if iop&0x80 != 0 {
			return profileConstrainedBaseline
		}
		return profileMain

	case 0x58:
		if iop&0xC0 == 0xC0 {
			return profileConstrainedBaseline
		}
		if iop&0x80 != 0 {
			return profileBaseline
		}
		return profileUnknown

	case 0x64:
		if iop&0x0C == 0x0C {
			return profileConstrainedHigh
		}
		if iop == 0x00 {
			return profileHigh
		}
		return profileUnknown
	}

	return profileUnknown
}

func isLevel1b(profileIDC, profileIOP, levelIDC uint8) bool {
	switch profileIDC {
	case 0x42, 0x4D, 0x58:
		return levelIDC == 11 && profileIOP&0x10 != 0
	default:
		return levelIDC == 9
	}
}

// gstH264ProfileName maps an RFC 6184 profile to the string GStreamer's
// h264parse/x264enc expect in `video/x-h264, profile=...`. Constrained
// High is collapsed to "high" because x264enc has no constrained-high
// mode and the two share profile_idc=0x64 (the csf4/csf5 flags only
// signal a tool subset, not a different bitstream profile).
func gstH264ProfileName(p profile) string {
	switch p {
	case profileConstrainedBaseline:
		return "constrained-baseline"
	case profileBaseline:
		return "baseline"
	case profileMain:
		return "main"
	case profileHigh, profileConstrainedHigh:
		return "high"
	}
	return ""
}

// gstH264LevelName maps an H.264 levelIDC (+ 1b flag) to the GStreamer
// `level=(string)X.Y` value.
func gstH264LevelName(levelIDC uint8, is1b bool) string {
	if is1b {
		return "1b"
	}
	switch levelIDC {
	case 10:
		return "1"
	case 11:
		return "1.1"
	case 12:
		return "1.2"
	case 13:
		return "1.3"
	case 20:
		return "2"
	case 21:
		return "2.1"
	case 22:
		return "2.2"
	case 30:
		return "3"
	case 31:
		return "3.1"
	case 32:
		return "3.2"
	case 40:
		return "4"
	case 41:
		return "4.1"
	case 42:
		return "4.2"
	case 50:
		return "5"
	case 51:
		return "5.1"
	case 52:
		return "5.2"
	}
	return ""
}

func gstH264LevelIDC(levelStr string) (uint8, bool) {
	switch levelStr {
	case "1":
		return 10, false
	case "1.1":
		return 11, false
	case "1.2":
		return 12, false
	case "1.3":
		return 13, false
	case "2":
		return 20, false
	case "2.1":
		return 21, false
	case "2.2":
		return 22, false
	case "3":
		return 30, false
	case "3.1":
		return 31, false
	case "3.2":
		return 32, false
	case "4":
		return 40, false
	case "4.1":
		return 41, false
	case "4.2":
		return 42, false
	case "5":
		return 50, false
	case "5.1":
		return 51, false
	case "5.2":
		return 52, false
	case "1b":
		return 11, true
	default:
		return 0, false
	}
}

// h264CapsStringForPLID returns a GStreamer caps string describing the
// H.264-domain constraint implied by the given profile-level-id. Empty
// string if plid cannot be parsed to a known profile/level.
func h264CapsStringForPLID(plid profileLevelID) string {
	profName := gstH264ProfileName(plid.profile)

	var levels []string
	for _, level := range h264Levels {
		if level.levelIDC <= plid.levelIDC && level.isLevel1b == plid.isLevel1b {
			levels = append(levels, gstH264LevelName(level.levelIDC, level.isLevel1b))
		}
	}
	if profName == "" || len(levels) == 0 {
		return ""
	}

	slices.Reverse(levels)

	return fmt.Sprintf(
		"video/x-h264, profile=(string)%s, level=(string){(string)%s}, stream-format=(string)avc, alignment=(string)au",
		profName, strings.Join(levels, ",(string)"),
	)
}

type h264LevelLimits struct {
	levelIDC  uint8
	isLevel1b bool
	maxFS     uint32
	maxMBPS   uint32
}

var h264Levels = []h264LevelLimits{
	{10, false, 99, 1485},
	{11, true, 99, 1485},
	{11, false, 396, 3000},
	{12, false, 396, 6000},
	{13, false, 396, 11880},
	{20, false, 396, 11880},
	{21, false, 792, 19800},
	{22, false, 1620, 20250},
	{30, false, 1620, 40500},
	{31, false, 3600, 108000},
	{32, false, 5120, 216000},
	{40, false, 8192, 245760},
	{41, false, 8192, 245760},
	{42, false, 8704, 522240},
	{50, false, 22080, 589824},
	{51, false, 36864, 983040},
	{52, false, 36864, 2073600},
}

func limitsForLevel(levelIDC uint8, isLevel1b bool) *h264LevelLimits {
	for i := range h264Levels {
		if h264Levels[i].levelIDC == levelIDC && h264Levels[i].isLevel1b == isLevel1b {
			return &h264Levels[i]
		}
	}
	return nil
}

// maxResolutionForLevel computes the maximum width and height (assuming
// 16:9 aspect ratio) that fit within the given profile-level-id's
// constraints at the specified framerate.
func maxResolutionForLevel(levelIDC uint8, isLevel1b bool, fps int) (maxWidth, maxHeight int, ok bool) {
	limits := limitsForLevel(levelIDC, isLevel1b)
	if limits == nil {
		return 0, 0, false
	}

	if fps <= 0 {
		fps = 30
	}

	effectiveFS := limits.maxFS
	mbpsFS := limits.maxMBPS / uint32(fps)
	if mbpsFS < effectiveFS {
		effectiveFS = mbpsFS
	}

	heightMBs := int(math.Sqrt(float64(effectiveFS) * 9.0 / 16.0))
	if heightMBs == 0 {
		return 0, 0, false
	}
	widthMBs := int(effectiveFS) / heightMBs

	return widthMBs * 16, heightMBs * 16, true
}

var levelThresholds = []struct {
	maxFS    int
	maxMBPS  int
	levelIDC uint8
}{
	{36864, 983040, 51},
	{22080, 589824, 50},
	{8704, 522240, 42},
	{8192, 245760, 41},
	{8192, 245760, 40},
	{5120, 216000, 32},
	{3600, 108000, 31},
	{1620, 40500, 30},
}

func patchProfileLevelID(parsed profileLevelID, maxFs, maxMbps int) profileLevelID {
	if maxFs <= 0 || maxMbps <= 0 {
		return parsed
	}

	for _, t := range levelThresholds {
		if maxFs*100 >= t.maxFS*95 && maxMbps*100 >= t.maxMBPS*95 && parsed.levelIDC < t.levelIDC {
			parsed.levelIDC = t.levelIDC
			return parsed
		}
	}

	return parsed
}
