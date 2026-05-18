package h264rtppaybin

import (
	"testing"

	"github.com/go-gst/go-gst/gst"
)

func TestParseProfileLevelID(t *testing.T) {
	tests := []struct {
		plid      string
		wantProf  profile
		wantLevel uint8
		want1b    bool
		wantErr   bool
	}{
		// Constrained Baseline 3.1 — common WebRTC baseline
		{"42e01f", profileConstrainedBaseline, 31, false, false},
		// Baseline (no constraint) 3.1
		{"42001f", profileBaseline, 31, false, false},
		// Main 3.1
		{"4d001f", profileMain, 31, false, false},
		// 640c1f is constrained-high per RFC 6184 (csf4+csf5 set); it
		// still encodes as profile_idc=0x64 so it maps to "high" for the
		// encoder's capsfilter (see gstH264ProfileName).
		{"640c1f", profileConstrainedHigh, 31, false, false},
		// High 4.2
		{"64002a", profileHigh, 42, false, false},
		// Level 1b (baseline family)
		{"42d00b", profileConstrainedBaseline, 11, true, false},
		// Malformed length
		{"640c", 0, 0, false, true},
		// Non-hex
		{"zzzzzz", 0, 0, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.plid, func(t *testing.T) {
			got, err := parseProfileLevelID(tc.plid)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (parsed=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.profile != tc.wantProf {
				t.Errorf("profile: got %d, want %d", got.profile, tc.wantProf)
			}
			if got.levelIDC != tc.wantLevel {
				t.Errorf("levelIDC: got %d, want %d", got.levelIDC, tc.wantLevel)
			}
			if got.isLevel1b != tc.want1b {
				t.Errorf("isLevel1b: got %v, want %v", got.isLevel1b, tc.want1b)
			}
		})
	}
}

func TestH264CapsStringForPLID(t *testing.T) {
	tests := []struct {
		plid        string
		wantProfile string
		wantLevel   string
	}{
		{"42e01f", "constrained-baseline", "3.1"},
		{"42001f", "baseline", "3.1"},
		{"4d001f", "main", "3.1"},
		{"640c1f", "high", "3.1"},
		{"64002a", "high", "4.2"},
		{"640c28", "high", "4"},
	}

	for _, tc := range tests {
		t.Run(tc.plid, func(t *testing.T) {
			parsed, err := parseProfileLevelID(tc.plid)
			if err != nil {
				t.Fatalf("failed to parse plid %q: %v", tc.plid, err)
			}
			s := h264CapsStringForPLID(parsed)
			if s == "" {
				t.Fatalf("got empty caps string for plid=%s", tc.plid)
			}
			caps := gst.NewCapsFromString(s)
			if caps == nil {
				t.Fatalf("failed to parse caps string %q", s)
			}

			if !gst.NewCapsFromString("video/x-h264,profile=(string)" + tc.wantProfile).CanIntersect(caps) {
				t.Errorf("expected profile=%q in %q", tc.wantProfile, caps.String())
			}
			if !gst.NewCapsFromString("video/x-h264,level=(string)" + tc.wantLevel).CanIntersect(caps) {
				t.Errorf("expected level=%q in %q", tc.wantLevel, caps.String())
			}
			if !gst.NewCapsFromString("video/x-h264,stream-format=(string)avc").CanIntersect(caps) {
				t.Errorf("expected stream-format=avc in %q", caps.String())
			}
			if !gst.NewCapsFromString("video/x-h264,alignment=(string)au").CanIntersect(caps) {
				t.Errorf("expected alignment=au in %q", caps.String())
			}
		})
	}
}

func TestH264CapsStringForPLID_Invalid(t *testing.T) {
	// Unknown profile/level combinations should yield empty caps.
	for _, plid := range []string{"000000", "ff00ff"} {
		parsed, err := parseProfileLevelID(plid)
		if err != nil {
			continue // parse failure is fine too
		}
		if got := h264CapsStringForPLID(parsed); got != "" {
			t.Errorf("plid=%q: expected empty, got %q", plid, got)
		}
	}
}

func TestMaxResolutionForLevel(t *testing.T) {
	tests := []struct {
		plid     string
		fps      int
		wantMinW int
		wantMinH int
	}{
		// Level 3.1: maxFS=3600 MBs. 16:9 -> ~1280x720 region.
		{"640c1f", 30, 900, 500},
		// Level 4.2: much higher, 1920x1088+ at 30fps
		{"64002a", 30, 1900, 1080},
		// Level 1.3: 396 MBs @ 16:9 -> ~448x224
		{"42c00d", 30, 400, 200},
	}
	for _, tc := range tests {
		t.Run(tc.plid, func(t *testing.T) {
			parsed, err := parseProfileLevelID(tc.plid)
			if err != nil {
				t.Fatalf("failed to parse plid %q: %v", tc.plid, err)
			}
			w, h, ok := maxResolutionForLevel(parsed.levelIDC, parsed.isLevel1b, tc.fps)
			if !ok {
				t.Fatalf("plid=%s: not ok", tc.plid)
			}
			if w < tc.wantMinW || h < tc.wantMinH {
				t.Errorf("plid=%s: got %dx%d, want at least %dx%d", tc.plid, w, h, tc.wantMinW, tc.wantMinH)
			}
		})
	}
}

func TestMaxResolutionForLevel_Invalid(t *testing.T) {
	// A plid whose level has no entry in h264Levels should return !ok.
	parsed, err := parseProfileLevelID("42e0ff")
	if err != nil {
		t.Skip("parse error, skipping")
	}
	if _, _, ok := maxResolutionForLevel(parsed.levelIDC, parsed.isLevel1b, 30); ok {
		t.Errorf("expected !ok for unknown level")
	}
}

func TestPatchProfileLevelID(t *testing.T) {
	tests := []struct {
		name      string
		plid      string
		maxFs     int
		maxMbps   int
		wantLevel uint8
	}{
		// No patching when maxFs/maxMbps are zero.
		{"no_patch_zero", "42e01f", 0, 0, 31},
		// No patching when constraints are negative.
		{"no_patch_negative", "42e01f", -1, -1, 31},
		// Constraints that match level 5.1 should upgrade from 3.1.
		{"upgrade_to_51", "42e01f", 36864, 983040, 51},
		// Constraints matching level 4.2 (with 95% tolerance).
		{"upgrade_to_42", "42e01f", 8270, 496328, 42},
		// Constraints already at level — no upgrade needed.
		{"no_upgrade_at_level", "64002a", 3600, 108000, 42},
		// Constraints that fit level 3.0 thresholds should upgrade from
		// a lower starting level.
		{"upgrade_to_30", "42e00a", 1620, 40500, 30},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseProfileLevelID(tc.plid)
			if err != nil {
				t.Fatalf("failed to parse plid %q: %v", tc.plid, err)
			}
			result := patchProfileLevelID(parsed, tc.maxFs, tc.maxMbps)
			if result.levelIDC != tc.wantLevel {
				t.Errorf("levelIDC: got %d, want %d", result.levelIDC, tc.wantLevel)
			}
		})
	}
}
