// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package livekitcompositor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/go-gst/go-gst/gst"
	"github.com/vopenia-io/go-pangocairo/cairo"
	"github.com/vopenia-io/go-pangocairo/pango"
)

func pixel(surf *cairo.Surface, x, y int) (r, g, b uint8) {
	stride := cairo.FormatStrideForWidth(cairo.FORMAT_RGB24, surf.GetWidth())
	data := unsafe.Slice((*byte)(surf.GetData()), stride*surf.GetHeight())
	i := y*stride + x*4
	return data[i+2], data[i+1], data[i]
}

func near(got uint8, want uint32, tolerance int) bool {
	d := int(got) - int(want)
	return d >= -tolerance && d <= tolerance
}

func slotX(ref float64) int { return int(ref * 1280 / 960) }

func colorIs(surf *cairo.Surface, x, y int, hex uint32, tolerance int) bool {
	r, g, b := pixel(surf, x, y)
	return near(r, hex>>16&0xff, tolerance) && near(g, hex>>8&0xff, tolerance) && near(b, hex&0xff, tolerance)
}

func columnHasColor(surf *cairo.Surface, x, y0, y1 int, hex uint32, tolerance int) bool {
	for y := y0; y < y1; y++ {
		if colorIs(surf, x, y, hex, tolerance) {
			return true
		}
	}
	return false
}

func entryScreen(entered string) *Screen {
	return &Screen{
		Eyebrow: "Rejoindre une réunion",
		Title:   "Tapez le code de la réunion, puis #",
		Digits:  &ScreenDigits{Entered: entered, Length: 10},
		Footer:  []ScreenHint{{Key: "*", Label: "Effacer"}},
	}
}

func rejectedScreen(entered string) *Screen {
	return &Screen{
		Tone:    ToneError,
		Eyebrow: "Rejoindre une réunion",
		Title:   "Code invalide",
		Body:    "Essai : 2/3. Vérifiez le code dans l’invitation, puis recommencez.",
		Digits:  &ScreenDigits{Entered: entered, Length: 10, Error: true},
		Footer:  []ScreenHint{{Key: "*", Label: "Recommencer"}},
	}
}

func allScreens() map[string]*Screen {
	return map[string]*Screen{
		"etape-1":  entryScreen(""),
		"etape-2":  entryScreen("1234567"),
		"etape-3":  rejectedScreen("1234567809"),
		"etape-4":  {Spinner: true, Caption: "Connexion à la réunion"},
		"etape-5":  {Tone: ToneError, Icon: IconClock, Title: "Délai dépassé", Body: "Aucune saisie depuis 60 secondes, l’appel va se terminer."},
		"etape-10": {Icon: IconPeople, Title: "En attente de participants", Body: "Les participants apparaîtront ici dès leur arrivée."},
		"etape-11": {Icon: IconScreenShareOff, Title: "Fin du partage d’écran", Body: "Les participants réapparaissent dans un instant."},
	}
}

func renderTestScreen(t *testing.T, s *Screen, w, h int) *screenRaster {
	t.Helper()
	registerFonts()
	r := newScreenRaster(w, h)
	t.Cleanup(r.releaseFonts)
	r.render(s)
	if status := r.cr.Status(); status != cairo.STATUS_SUCCESS {
		t.Fatalf("cairo status %v", status)
	}
	return r
}

func TestScreen_WritesPNGWhenAsked(t *testing.T) {
	dir := os.Getenv("LKSIP_SCREENS_DIR")
	if dir == "" {
		t.Skip("LKSIP_SCREENS_DIR not set")
	}
	for name, s := range allScreens() {
		r := renderTestScreen(t, s, 1280, 720)
		if s.Spinner {
			r.paintSpinnerOnRaster(0)
		}
		if err := r.surface.WriteToPNG(filepath.Join(dir, name+".png")); err != nil {
			t.Fatal(err)
		}
	}
}

func (r *screenRaster) paintSpinnerOnRaster(ts gst.ClockTime) {
	r.paintSpinner(r.cr, ts)
	r.surface.Flush()
}

func TestScreen_BackgroundFollowsTheTone(t *testing.T) {
	accent := renderTestScreen(t, entryScreen(""), 1280, 720).surface
	if !colorIs(accent, 40, 680, 0x161B2E, 1) {
		r, g, b := pixel(accent, 40, 680)
		t.Errorf("bottom-left is %02x%02x%02x, want the darkest blue", r, g, b)
	}
	if !colorIs(accent, 1220, 60, 0x1E2336, 1) {
		r, g, b := pixel(accent, 1220, 60)
		t.Errorf("top-right is %02x%02x%02x, want a lighter blue", r, g, b)
	}
	failed := renderTestScreen(t, rejectedScreen("12"), 1280, 720).surface
	if !colorIs(failed, 1220, 60, 0x301D23, 1) {
		r, g, b := pixel(failed, 1220, 60)
		t.Errorf("top-right is %02x%02x%02x, want a lighter red", r, g, b)
	}
}

func TestScreen_BackgroundKeepsOneTint(t *testing.T) {
	for _, s := range []*Screen{
		allScreens()["etape-10"],
		allScreens()["etape-5"],
	} {
		surf := renderTestScreen(t, s, 1280, 720).surface
		tint := s.Tone.gradient().tint
		wantRG := int(tint>>16&0xff) - int(tint>>8&0xff)
		wantBG := int(tint&0xff) - int(tint>>8&0xff)
		check := func(x, y int) {
			r, g, b := pixel(surf, x, y)
			if int(r)-int(g) != wantRG || int(b)-int(g) != wantBG {
				t.Fatalf("tone %d at %d,%d is %02x%02x%02x, want the tint %06x shifted on all channels", s.Tone, x, y, r, g, b, tint)
			}
		}
		for x := range 1280 {
			check(x, 700)
		}
		for y := range 720 {
			check(1270, y)
		}
	}
}

func TestScreen_BackgroundIsDithered(t *testing.T) {
	for _, s := range []*Screen{
		allScreens()["etape-10"],
		allScreens()["etape-5"],
	} {
		surf := renderTestScreen(t, s, 1280, 720).surface
		mixed, blocks, last := 0, 0, 0
		for y := 718; y >= 0; y -= 2 {
			lo, hi, sum := 255, 0, 0
			for _, p := range [][2]int{{1270, y}, {1271, y}, {1270, y + 1}, {1271, y + 1}} {
				_, g, _ := pixel(surf, p[0], p[1])
				lo, hi, sum = min(lo, int(g)), max(hi, int(g)), sum+int(g)
			}
			if hi-lo > 1 {
				t.Fatalf("tone %d: the pixels of the block at y=%d span %d levels", s.Tone, y, hi-lo)
			}
			if y < 718 && sum < last {
				t.Fatalf("tone %d: the block at y=%d is darker than the one below", s.Tone, y)
			}
			last = sum
			blocks++
			if hi > lo {
				mixed++
			}
		}
		if mixed*2 < blocks {
			t.Errorf("tone %d: only %d of %d blocks mix two levels", s.Tone, mixed, blocks)
		}
	}
}

func TestScreen_DigitSlotsAtTheMockupPositions(t *testing.T) {
	surf := renderTestScreen(t, entryScreen("12"), 1280, 720).surface
	const lineY0, lineY1 = 405, 416
	if !columnHasColor(surf, slotX(284+12), lineY0, lineY1, 0xEEF1F4, 2) {
		t.Error("no entered underline under the first position")
	}
	if !columnHasColor(surf, slotX(284+2*32+12), lineY0, lineY1, 0xA2B6FF, 2) {
		t.Error("no accent underline under the current position")
	}
	if !columnHasColor(surf, slotX(284+112+12), lineY0, lineY1, 0x555E74, 2) {
		t.Error("no disabled underline under a later position")
	}
	if !columnHasColor(surf, slotX(284+368+12), lineY0, lineY1, 0x555E74, 2) {
		t.Error("no disabled underline under the # position")
	}
}

func TestScreen_RejectedDigitsUseTheErrorColors(t *testing.T) {
	surf := renderTestScreen(t, rejectedScreen("1234567809"), 1280, 720).surface
	x := slotX(284 + 12)
	if !columnHasColor(surf, x, 405, 416, 0xFF9F99, 2) {
		t.Error("no error underline under the first digit")
	}
	hash := slotX(284 + 368 + 12)
	if !columnHasColor(surf, hash, 405, 416, 0xFF9F99, 2) {
		t.Error("no error underline under the # position")
	}
}

func TestScreen_FullCodeMovesTheAccentToHash(t *testing.T) {
	surf := renderTestScreen(t, entryScreen("1234567890"), 1280, 720).surface
	hash := slotX(284 + 368 + 12)
	if !columnHasColor(surf, hash, 405, 416, 0xA2B6FF, 2) {
		t.Error("no accent underline under the # position")
	}
}

func TestScreen_IconsAreDrawn(t *testing.T) {
	for name, s := range allScreens() {
		if s.Icon == IconNone {
			continue
		}
		surf := renderTestScreen(t, s, 1280, 720).surface
		inked := 0
		for y := 200; y < 420; y++ {
			if colorIs(surf, 640, y, 0xA2B6FF, 8) || colorIs(surf, 640, y, 0xFF9F99, 8) {
				inked++
			}
		}
		if inked == 0 {
			t.Errorf("%s: no icon pixel on the center column", name)
		}
	}
}

func TestScreen_LogoIsDrawn(t *testing.T) {
	surf := renderTestScreen(t, entryScreen(""), 1280, 720).surface
	found := false
	for x := 90; x < 215 && !found; x++ {
		found = columnHasColor(surf, x, 55, 105, 0xEEF1F4, 4)
	}
	if !found {
		t.Error("no logo pixel in the logo area")
	}
}

func TestScreen_FontWeightsAndDigits(t *testing.T) {
	r := renderTestScreen(t, entryScreen(""), 1280, 720)
	const text = "Tapez le code de la réunion"
	regular := r.textWidth(textStyle{32, 40, pango.WEIGHT_NORMAL}, text)
	bold := r.textWidth(textStyle{32, 40, pango.WEIGHT_BOLD}, text)
	if bold <= regular {
		t.Errorf("bold width %.1f is not above the regular width %.1f, the weight axis is not applied", bold, regular)
	}
	if one, zero := r.textWidth(styleTitle, "1"), r.textWidth(styleTitle, "0"); one != zero {
		t.Errorf("digits are not tabular: 1=%.2f 0=%.2f", one, zero)
	}
}

func TestScreen_DrawKeepsOneRasterAndRendersOnChange(t *testing.T) {
	registerFonts()
	e := &LivekitCompositor{videoWidth: 1280, videoHeight: 720, lang: "fr"}
	e.LivekitCompositorCamera = &LivekitCompositorCamera{}
	defer e.LivekitCompositorCamera.releaseScreenRaster()
	target := cairo.CreateImageSurface(cairo.FORMAT_RGB24, 1280, 720)
	cr := cairo.Create(target)

	first := entryScreen("")
	e.drawScreen(cr, first, 1280, 720, 0)
	raster := e.LivekitCompositorCamera.screenRaster
	surface := raster.surface
	r, g, b := pixel(target, 1220, 60)
	if !colorIs(surface, 1220, 60, uint32(r)<<16|uint32(g)<<8|uint32(b), 0) {
		t.Fatal("the raster was not painted on the target")
	}
	e.drawScreen(cr, first, 1280, 720, 0)
	if e.LivekitCompositorCamera.screenShown != first {
		t.Fatal("the shown screen was lost")
	}
	second := rejectedScreen("12")
	e.drawScreen(cr, second, 1280, 720, 0)
	if e.LivekitCompositorCamera.screenRaster != raster || raster.surface != surface {
		t.Fatal("a new screen allocated a new raster")
	}
	if e.LivekitCompositorCamera.screenShown != second {
		t.Fatal("the new screen was not rendered")
	}
	target.Flush()
	if !colorIs(target, 1220, 60, 0x2D1C25, 3) {
		t.Error("the target does not show the new screen")
	}
	e.drawScreen(cr, first, 640, 360, 0)
	if e.LivekitCompositorCamera.screenRaster == raster {
		t.Error("a new size kept the old raster")
	}
}

func TestScreen_SpinnerTurnsOncePerSecond(t *testing.T) {
	s := &Screen{Spinner: true, Caption: "Connexion à la réunion"}
	registerFonts()
	raster := renderTestScreen(t, s, 1280, 720)
	render := func(ts gst.ClockTime) []byte {
		target := cairo.CreateImageSurface(cairo.FORMAT_RGB24, 1280, 720)
		cr := cairo.Create(target)
		raster.paintOn(cr)
		raster.paintSpinner(cr, ts)
		target.Flush()
		stride := cairo.FormatStrideForWidth(cairo.FORMAT_RGB24, 1280)
		return append([]byte(nil), unsafe.Slice((*byte)(target.GetData()), stride*720)...)
	}
	start := render(gst.ClockTime(3 * time.Second))
	quarter := render(gst.ClockTime(3*time.Second + 250*time.Millisecond))
	again := render(gst.ClockTime(4 * time.Second))
	if string(start) == string(quarter) {
		t.Error("the spinner did not move after 250 ms")
	}
	if string(start) != string(again) {
		t.Error("the spinner is not back at its start after one second")
	}
	render(gst.ClockTimeNone)
}

func TestScreen_BodyLinesAreBalanced(t *testing.T) {
	r := renderTestScreen(t, entryScreen(""), 1280, 720)
	lines := r.balancedLines(styleBody, "Aucune saisie depuis 60 secondes, l’appel va se terminer.", screenBodyMaxW)
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	first, second := r.textWidth(styleBody, lines[0]), r.textWidth(styleBody, lines[1])
	if diff := first - second; diff > 100 || diff < -100 {
		t.Errorf("unbalanced lines %q: %.0f and %.0f", lines, first, second)
	}
	one := r.balancedLines(styleBody, "Les participants apparaîtront ici dès leur arrivée.", screenBodyMaxW)
	if len(one) != 1 {
		t.Errorf("a line fitting the column was split: %q", one)
	}
}

func TestScreen_FitsOtherFrameSizes(t *testing.T) {
	for _, size := range [][2]int{{320, 240}, {640, 360}, {1920, 1080}, {1280, 800}} {
		renderTestScreen(t, allScreens()["etape-11"], size[0], size[1])
	}
}

func rssKB(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Skip("no /proc/self/statm")
	}
	fields := strings.Fields(string(data))
	pages, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	return pages * os.Getpagesize() / 1024
}

func TestScreen_RepeatedRendersAndFramesDoNotGrow(t *testing.T) {
	registerFonts()
	r := renderTestScreen(t, entryScreen(""), 1280, 720)
	target := cairo.CreateImageSurface(cairo.FORMAT_RGB24, 1280, 720)
	cr := cairo.Create(target)
	spin := &Screen{Spinner: true, Caption: "Connexion à la réunion"}
	work := func(n int) {
		for i := range n {
			r.render(entryScreen(strings.Repeat("1", i%11)))
			r.render(spin)
			r.paintOn(cr)
			r.paintSpinner(cr, gst.ClockTime(i)*gst.ClockTime(40*time.Millisecond))
		}
	}
	work(500)
	before := rssKB(t)
	work(3000)
	if grown := rssKB(t) - before; grown > 16*1024 {
		t.Errorf("RSS grew by %d kB over 3000 rounds", grown)
	}
}

func TestScreen_AloneIsLocalized(t *testing.T) {
	e := &LivekitCompositor{lang: "fr"}
	if got := e.aloneScreen().Title; got != "En attente de participants" {
		t.Errorf("fr title = %q", got)
	}
	e.lang = "en"
	if got := e.aloneScreen().Title; got != "Waiting for participants" {
		t.Errorf("en title = %q", got)
	}
}
