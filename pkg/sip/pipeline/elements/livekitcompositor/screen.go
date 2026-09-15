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
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/livekit/sip/pkg/i18n"
	"github.com/livekit/sip/res"
	"github.com/vopenia-io/go-pangocairo/cairo"
	"github.com/vopenia-io/go-pangocairo/pango"
	"golang.org/x/sys/unix"
)

type ScreenTone int

const (
	ToneAccent ScreenTone = iota
	ToneError
	ToneSuccess
	ToneMuted
)

type ScreenIcon string

const (
	IconNone      ScreenIcon = ""
	IconHourglass ScreenIcon = "hourglass"
	IconPerson    ScreenIcon = "person"
	IconCheck     ScreenIcon = "check"
	IconCross     ScreenIcon = "cross"
	IconClock     ScreenIcon = "clock"
	IconPeople    ScreenIcon = "people"
)

// ScreenHint is a footer item: a key cap followed by a label, or a label alone.
type ScreenHint struct {
	Key   string `json:"key,omitempty"`
	Label string `json:"label"`
}

// ScreenDigits is the code entry row: Length positions, Entered digits shown in clear.
type ScreenDigits struct {
	Entered string `json:"entered"`
	Length  int    `json:"length"`
	Error   bool   `json:"error,omitempty"`
}

// Screen is a full-frame page drawn in place of the mosaic: logo top-left,
// centered column (icon, eyebrow, title, body, digits), hints in the footer.
type Screen struct {
	Icon        ScreenIcon    `json:"icon,omitempty"`
	IconTone    ScreenTone    `json:"icon_tone,omitempty"`
	Eyebrow     string        `json:"eyebrow,omitempty"`
	EyebrowTone ScreenTone    `json:"eyebrow_tone,omitempty"`
	Title       string        `json:"title"`
	Body        string        `json:"body,omitempty"`
	Digits      *ScreenDigits `json:"digits,omitempty"`
	Footer      []ScreenHint  `json:"footer,omitempty"`
}

func (s *Screen) key() string {
	data, _ := json.Marshal(s)
	return string(data)
}

var logoSurface *cairo.Surface

func init() {
	data, err := assets.ReadFile("assets/logo-visio.png")
	if err != nil {
		panic(fmt.Sprintf("Failed to read embedded logo: %v", err))
	}
	fd, err := res.MemfdFromBytes("assets/logo-visio.png", data)
	if err != nil {
		panic(fmt.Sprintf("Failed to create memfd for logo: %v", err))
	}
	defer unix.Close(fd)
	logoSurface, err = cairo.NewSurfaceFromPNG(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		panic(fmt.Sprintf("Failed to create cairo surface for logo: %v", err))
	}
}

// aloneScreen replaces the mosaic while no participant publishes a track.
func (e *LivekitCompositor) aloneScreen() *Screen {
	p := i18n.Printer(e.lang)
	return &Screen{
		Icon:     IconPeople,
		IconTone: ToneMuted,
		Title:    p.Sprintf("Nobody else is here yet"),
		Body:     p.Sprintf("Participants will appear here as they arrive."),
	}
}

// drawScreen paints the cached raster of s, re-rendering it only when the screen changed.
func (e *LivekitCompositor) drawScreen(cr *cairo.Context, s *Screen, w, h int) {
	c := e.LivekitCompositorCamera
	key := s.key()
	if c.screenSurface == nil || c.screenKey != key || c.screenSurface.GetWidth() != w || c.screenSurface.GetHeight() != h {
		c.screenSurface = e.renderScreen(s, w, h)
		c.screenKey = key
	}
	cr.Save()
	cr.SetSourceSurface(c.screenSurface, 0, 0)
	cr.Paint()
	cr.Restore()
}

func (e *LivekitCompositor) renderScreen(s *Screen, w, h int) *cairo.Surface {
	surf := cairo.CreateImageSurface(cairo.FORMAT_ARGB32, w, h)
	cr := cairo.Create(surf)
	scale := float64(w) / screenRefW
	cr.Scale(scale, scale)
	p := &screenPainter{e: e, cr: cr, w: screenRefW, h: float64(h) / scale}
	p.paint(s)
	surf.Flush()
	return surf
}

type screenPainter struct {
	e    *LivekitCompositor
	cr   *cairo.Context
	w, h float64
}

// textStyle selects the font family, weight and pixel size of a text run.
type textStyle struct {
	px    float64
	style fontStyle
}

var (
	styleEyebrow = textStyle{screenEyebrowPx, fontSansBold}
	styleTitle   = textStyle{screenTitlePx, fontSansBold}
	styleBody    = textStyle{screenBodyPx, fontSans}
	styleFooter  = textStyle{screenFooterPx, fontSans}
	styleKey     = textStyle{screenKeyPx, fontMonoBold}
	styleDigit   = textStyle{screenDigitPx, fontMonoBold}
)

// layout binds the shared pango layout to st and text, and returns its size.
func (p *screenPainter) layout(st textStyle, text string) (*pango.Layout, float64, float64) {
	layout, desc := p.e.layoutFor(p.cr, st.style)
	desc.SetAbsoluteSize(st.px * float64(pango.SCALE))
	layout.SetFontDescription(desc)
	layout.SetText(text, -1)
	w, h := layout.GetSize()
	return layout, float64(w) / float64(pango.SCALE), float64(h) / float64(pango.SCALE)
}

func (p *screenPainter) measure(st textStyle, text string) (float64, float64) {
	_, w, h := p.layout(st, text)
	return w, h
}

// wrap breaks text into lines no wider than maxW, keeping explicit newlines.
func (p *screenPainter) wrap(st textStyle, text string, maxW float64) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			candidate := line + " " + word
			if w, _ := p.measure(st, candidate); w > maxW {
				lines = append(lines, line)
				line = word
			} else {
				line = candidate
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func (p *screenPainter) lineHeight(st textStyle) float64 {
	_, h := p.measure(st, "Hg")
	return h
}

func (p *screenPainter) setColor(c rgb) {
	p.cr.SetSourceRGB(c.r, c.g, c.b)
}

// text draws one run with its top-left corner at (x, y).
func (p *screenPainter) text(st textStyle, s string, c rgb, x, y float64) {
	layout, _, _ := p.layout(st, s)
	p.setColor(c)
	p.cr.MoveTo(x, y)
	pango.CairoShowLayout(p.cr, layout)
}

// textLines draws centered lines from y down and returns the y below the block.
func (p *screenPainter) textLines(st textStyle, lines []string, lead float64, c rgb, cx, y float64) float64 {
	for _, line := range lines {
		w, h := p.measure(st, line)
		lineH := h * lead
		p.text(st, line, c, cx-w/2, y+(lineH-h)/2)
		y += lineH
	}
	return y
}

func (p *screenPainter) paint(s *Screen) {
	p.cr.Rectangle(0, 0, p.w, p.h)
	p.setColor(screenBg)
	p.cr.Fill()
	p.logo()

	cx := p.w / 2
	areaTop := screenMarginY + screenTopBarH
	areaBottom := p.h - screenMarginY - screenFooterH

	eyebrowH := p.lineHeight(styleEyebrow)
	titleH := p.lineHeight(styleTitle) * screenTitleLead
	bodyH := p.lineHeight(styleBody) * screenBodyLead
	titleLines := p.wrap(styleTitle, s.Title, screenColumnW)
	var bodyLines []string
	if s.Body != "" {
		bodyLines = p.wrap(styleBody, s.Body, screenColumnW)
	}

	total := float64(len(titleLines)) * titleH
	if s.Icon != IconNone {
		total += screenIconSize + screenIconGap
	}
	if s.Eyebrow != "" {
		total += eyebrowH + screenEyebrowGap
	}
	if len(bodyLines) > 0 {
		total += screenBodyGap + float64(len(bodyLines))*bodyH
	}
	if s.Digits != nil {
		total += screenDigitsTop + screenDigitH
	}

	y := areaTop + math.Max(0, (areaBottom-areaTop-total)/2)
	if s.Icon != IconNone {
		drawScreenIcon(p.cr, s.Icon, cx-screenIconSize/2, y, screenIconSize, s.IconTone.color())
		y += screenIconSize + screenIconGap
	}
	if s.Eyebrow != "" {
		y = p.textLines(styleEyebrow, []string{s.Eyebrow}, 1, s.EyebrowTone.color(), cx, y)
		y += screenEyebrowGap
	}
	y = p.textLines(styleTitle, titleLines, screenTitleLead, screenInk, cx, y)
	if len(bodyLines) > 0 {
		y += screenBodyGap
		y = p.textLines(styleBody, bodyLines, screenBodyLead, screenDim, cx, y)
	}
	if s.Digits != nil {
		p.digits(s.Digits, cx, y+screenDigitsTop)
	}
	if len(s.Footer) > 0 {
		p.footer(s.Footer, cx, areaBottom)
	}
}

func (p *screenPainter) logo() {
	k := screenTopBarH / float64(logoSurface.GetHeight())
	p.cr.Save()
	p.cr.Translate(screenMarginX, screenMarginY)
	p.cr.Scale(k, k)
	p.cr.SetSourceSurface(logoSurface, 0, 0)
	p.cr.Paint()
	p.cr.Restore()
}

func (p *screenPainter) digits(d *ScreenDigits, cx, y float64) {
	groups := digitGroups(d.Length)
	total := float64(d.Length)*screenDigitW + float64(d.Length-len(groups))*screenDigitGap + float64(len(groups)-1)*screenDigitGroupGap
	x := cx - total/2
	entered := len(d.Entered)
	idx := 0
	for gi, g := range groups {
		for k := 0; k < g; k++ {
			line, lineW := screenLine, screenDigitLine
			switch {
			case d.Error:
				line = screenErrLine
			case idx == entered:
				line, lineW = screenAccent, screenDigitLineCur
			case idx < entered:
				line = screenInk
			}
			p.cr.Rectangle(x, y+screenDigitH-lineW, screenDigitW, lineW)
			p.setColor(line)
			p.cr.Fill()
			if idx < entered {
				ink := screenInk
				if d.Error {
					ink = screenError
				}
				digit := d.Entered[idx : idx+1]
				w, h := p.measure(styleDigit, digit)
				p.text(styleDigit, digit, ink, x+(screenDigitW-w)/2, y+(screenDigitH-lineW-h)/2)
			}
			x += screenDigitW
			if k < g-1 {
				x += screenDigitGap
			}
			idx++
		}
		if gi < len(groups)-1 {
			x += screenDigitGroupGap
		}
	}
}

func (p *screenPainter) footer(hints []ScreenHint, cx, y float64) {
	widths := make([]float64, len(hints))
	total := screenFooterGap * float64(len(hints)-1)
	for i, h := range hints {
		w, _ := p.measure(styleFooter, h.Label)
		if h.Key != "" {
			w += screenKeySize + screenKeyGap
		}
		widths[i] = w
		total += w
	}
	x := cx - total/2
	for i, h := range hints {
		start := x
		if h.Key != "" {
			p.keycap(h.Key, x, y)
			x += screenKeySize + screenKeyGap
		}
		_, th := p.measure(styleFooter, h.Label)
		p.text(styleFooter, h.Label, screenDim, x, y+(screenFooterH-th)/2)
		x = start + widths[i] + screenFooterGap
	}
}

func (p *screenPainter) keycap(ch string, x, y float64) {
	roundedRectPath(p.cr, x+1, y+1, screenKeySize-2, screenKeySize-2, screenKeyRadius)
	p.setColor(screenWhite)
	p.cr.FillPreserve()
	p.setColor(screenLine)
	p.cr.SetLineWidth(2)
	p.cr.Stroke()
	w, h := p.measure(styleKey, ch)
	p.text(styleKey, ch, screenInk, x+(screenKeySize-w)/2, y+(screenKeySize-h)/2)
}

func roundedRectPath(cr *cairo.Context, x, y, w, h, r float64) {
	if maxR := math.Min(w, h) / 2; r > maxR {
		r = maxR
	}
	cr.MoveTo(x+w-r, y)
	cr.Arc(x+w-r, y+r, r, -math.Pi/2, 0)
	cr.Arc(x+w-r, y+h-r, r, 0, math.Pi/2)
	cr.Arc(x+r, y+h-r, r, math.Pi/2, math.Pi)
	cr.Arc(x+r, y+r, r, math.Pi, 3*math.Pi/2)
	cr.ClosePath()
}
