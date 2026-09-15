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
	"testing"
	"unsafe"

	"github.com/vopenia-io/go-pangocairo/cairo"
)

func pixel(surf *cairo.Surface, x, y int) (r, g, b uint8) {
	stride := cairo.FormatStrideForWidth(cairo.FORMAT_ARGB32, surf.GetWidth())
	data := unsafe.Slice((*byte)(surf.GetData()), stride*surf.GetHeight())
	i := y*stride + x*4
	return data[i+2], data[i+1], data[i]
}

func isBackground(r, g, b uint8) bool {
	return r == 0xF5 && g == 0xF5 && b == 0xFE
}

func testCompositor() *LivekitCompositor {
	registerFonts()
	return &LivekitCompositor{videoWidth: 1280, videoHeight: 720, lang: "fr"}
}

func TestScreen_CodeEntryRaster(t *testing.T) {
	e := testCompositor()
	s := &Screen{
		Eyebrow: "Rejoindre une réunion",
		Title:   "Tapez le code de la réunion, puis #",
		Digits:  &ScreenDigits{Entered: "12", Length: 10},
		Footer:  []ScreenHint{{Key: "#", Label: "Valider"}, {Key: "*", Label: "Effacer"}},
	}
	surf := e.renderScreen(s, 1280, 720)
	if surf.GetWidth() != 1280 || surf.GetHeight() != 720 {
		t.Fatalf("surface is %dx%d", surf.GetWidth(), surf.GetHeight())
	}
	for _, pt := range [][2]int{{2, 2}, {1277, 2}, {2, 717}, {1277, 717}} {
		if r, g, b := pixel(surf, pt[0], pt[1]); !isBackground(r, g, b) {
			t.Errorf("corner %v is %02x%02x%02x, want background", pt, r, g, b)
		}
	}
	// Digits row: 10 cells of 62 px, gaps 10, group gaps 34 → 758 px centered → first cell at x=261.
	// The third position is the current one and carries the accent underline.
	const thirdCellX = 261 + 2*(62+10) + 31
	accent, ink := false, false
	for y := 84; y < 640; y++ {
		r, g, b := pixel(surf, thirdCellX, y)
		if r == 0x00 && g == 0x00 && b == 0x91 {
			accent = true
		}
		r, g, b = pixel(surf, 261+31, y)
		if r == 0x16 && g == 0x16 && b == 0x16 {
			ink = true
		}
	}
	if !accent {
		t.Error("no accent underline under the current position")
	}
	if !ink {
		t.Error("no ink under the first entered digit")
	}
}

func TestScreen_ErrorTone(t *testing.T) {
	e := testCompositor()
	s := &Screen{
		Eyebrow:     "Code non reconnu",
		EyebrowTone: ToneError,
		Title:       "Aucune réunion ne correspond à ce code",
		Digits:      &ScreenDigits{Entered: "1234567809", Length: 10, Error: true},
	}
	surf := e.renderScreen(s, 1280, 720)
	found := false
	for y := 84; y < 640 && !found; y++ {
		r, g, b := pixel(surf, 261+31, y)
		found = r == 0xEE && g == 0x6A && b == 0x66
	}
	if !found {
		t.Error("no error underline under the first digit")
	}
}

func TestScreen_IconAndBody(t *testing.T) {
	e := testCompositor()
	s := &Screen{Icon: IconHourglass, Title: "Demande envoyée", Body: "Vous entrerez dans la réunion dès qu'une personne présente aura accepté votre demande."}
	surf := e.renderScreen(s, 1280, 720)
	inked := 0
	for y := 84; y < 640; y += 2 {
		for x := 190; x < 1090; x += 2 {
			if r, g, b := pixel(surf, x, y); !isBackground(r, g, b) {
				inked++
			}
		}
	}
	if inked < 2000 {
		t.Errorf("only %d inked samples in the column, drawing looks empty", inked)
	}
}

func TestScreen_DrawUsesCachedRaster(t *testing.T) {
	e := testCompositor()
	e.LivekitCompositorCamera = &LivekitCompositorCamera{}
	target := cairo.CreateImageSurface(cairo.FORMAT_ARGB32, 1280, 720)
	cr := cairo.Create(target)

	s := &Screen{Title: "A"}
	e.drawScreen(cr, s, 1280, 720)
	first := e.LivekitCompositorCamera.screenSurface
	if first == nil {
		t.Fatal("no raster after first draw")
	}
	e.drawScreen(cr, &Screen{Title: "A"}, 1280, 720)
	if e.LivekitCompositorCamera.screenSurface != first {
		t.Fatal("unchanged screen was re-rendered")
	}
	e.drawScreen(cr, &Screen{Title: "B"}, 1280, 720)
	if e.LivekitCompositorCamera.screenSurface == first {
		t.Fatal("changed screen kept the old raster")
	}
	target.Flush()
	if r, g, b := pixel(target, 2, 2); !isBackground(r, g, b) {
		t.Errorf("target corner is %02x%02x%02x, want background", r, g, b)
	}
}

func TestScreen_AloneIsLocalized(t *testing.T) {
	e := testCompositor()
	if got := e.aloneScreen().Title; got != "Personne d'autre n'est encore là" {
		t.Errorf("fr title = %q", got)
	}
	e.lang = "en"
	if got := e.aloneScreen().Title; got != "Nobody else is here yet" {
		t.Errorf("en title = %q", got)
	}
}
