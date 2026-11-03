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
	"math"
	"strings"
	"time"
	"unsafe"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/sip/pkg/i18n"
	"github.com/vopenia-io/go-pangocairo/cairo"
	"github.com/vopenia-io/go-pangocairo/pango"
)

type ScreenTone int

const (
	ToneAccent ScreenTone = iota
	ToneError
)

type ScreenIcon string

const (
	IconNone           ScreenIcon = ""
	IconClock          ScreenIcon = "clock"
	IconPeople         ScreenIcon = "people"
	IconScreenShareOff ScreenIcon = "screen-share-off"
)

type ScreenHint struct {
	Key   string `json:"key,omitempty"`
	Label string `json:"label"`
}

type ScreenDigits struct {
	Entered string `json:"entered"`
	Length  int    `json:"length"`
	Error   bool   `json:"error,omitempty"`
}

type Screen struct {
	Tone    ScreenTone    `json:"tone,omitempty"`
	Icon    ScreenIcon    `json:"icon,omitempty"`
	Spinner bool          `json:"spinner,omitempty"`
	Eyebrow string        `json:"eyebrow,omitempty"`
	Title   string        `json:"title,omitempty"`
	Caption string        `json:"caption,omitempty"`
	Body    string        `json:"body,omitempty"`
	Digits  *ScreenDigits `json:"digits,omitempty"`
	Footer  []ScreenHint  `json:"footer,omitempty"`
}

func (e *LivekitCompositor) aloneScreen() *Screen {
	p := i18n.Printer(e.lang)
	return &Screen{
		Icon:  IconPeople,
		Title: p.Sprintf("Waiting for participants"),
		Body:  p.Sprintf("Participants will appear here as they arrive."),
	}
}

func endOfShareScreen(lang string) *Screen {
	p := i18n.Printer(lang)
	return &Screen{
		Icon:  IconScreenShareOff,
		Title: p.Sprintf("Screen sharing has ended"),
		Body:  p.Sprintf("Participants will reappear in a moment."),
	}
}

func (e *LivekitCompositor) drawScreen(cr *cairo.Context, s *Screen, w, h int, ts gst.ClockTime) {
	c := e.LivekitCompositorCamera
	if c.screenRaster == nil || c.screenRaster.w != w || c.screenRaster.h != h {
		c.releaseScreenRaster()
		c.screenRaster = newScreenRaster(w, h)
	}
	if c.screenShown != s {
		c.screenRaster.render(s)
		c.screenShown = s
	}
	c.screenRaster.paintOn(cr)
	if s.Spinner {
		c.screenRaster.paintSpinner(cr, ts)
	}
}

func (c *LivekitCompositorCamera) releaseScreenRaster() {
	if c.screenRaster != nil {
		c.screenRaster.releaseFonts()
		c.screenRaster = nil
		c.screenShown = nil
	}
}

type screenRaster struct {
	surface *cairo.Surface
	cr      *cairo.Context

	layout  *pango.Layout
	regular *pango.FontDescription
	medium  *pango.FontDescription
	bold    *pango.FontDescription

	w, h       int
	scale      float64
	refW, refH float64
	spinnerX   float64
	spinnerY   float64
}

func newScreenRaster(w, h int) *screenRaster {
	surface := cairo.CreateImageSurface(cairo.FORMAT_RGB24, w, h)
	cr := cairo.Create(surface)
	scale := math.Min(float64(w)/screenRefW, float64(h)/screenRefH)
	return &screenRaster{
		surface: surface,
		cr:      cr,
		layout:  pango.CairoCreateLayout(cr),
		regular: newFontDesc(pango.WEIGHT_NORMAL),
		medium:  newFontDesc(pango.WEIGHT_MEDIUM),
		bold:    newFontDesc(pango.WEIGHT_BOLD),
		w:       w,
		h:       h,
		scale:   scale,
		refW:    float64(w) / scale,
		refH:    float64(h) / scale,
	}
}

func (r *screenRaster) releaseFonts() {
	if r.layout == nil {
		return
	}
	glib.TransferFull(unsafe.Pointer(r.layout.Native()))
	r.layout = nil
	r.regular.Free()
	r.medium.Free()
	r.bold.Free()
}

func (r *screenRaster) render(s *Screen) {
	r.cr.Save()
	pango.CairoUpdateLayout(r.cr, r.layout)
	r.cr.Scale(r.scale, r.scale)
	r.background(s.Tone)
	r.fillPaths(logoPaths, cairo.FILL_RULE_WINDING, screenInk, screenLogoX, screenLogoY, 1)
	switch {
	case s.Spinner:
		r.spinnerColumn(s)
	case s.Digits != nil:
		r.entryColumn(s)
	default:
		r.statusColumn(s)
	}
	if len(s.Footer) > 0 {
		r.footer(s.Footer)
	}
	r.cr.Restore()
	r.surface.Flush()
}

func (r *screenRaster) paintOn(cr *cairo.Context) {
	cr.Save()
	cr.SetSourceSurface(r.surface, 0, 0)
	cr.Paint()
	cr.Restore()
}

func (r *screenRaster) paintSpinner(cr *cairo.Context, ts gst.ClockTime) {
	angle := screenSpinnerStart
	if ts != gst.ClockTimeNone {
		angle += 2 * math.Pi * float64(uint64(ts)%uint64(time.Second)) / float64(time.Second)
	}
	cr.Save()
	cr.Scale(r.scale, r.scale)
	cr.NewPath()
	cr.Arc(r.spinnerX, r.spinnerY, screenSpinnerRadius, angle, angle+screenSpinnerSweep)
	cr.SetLineWidth(screenSpinnerStroke)
	cr.SetLineCap(cairo.LINE_CAP_ROUND)
	cr.SetSourceRGB(screenSpinnerFront.r, screenSpinnerFront.g, screenSpinnerFront.b)
	cr.Stroke()
	cr.Restore()
}

func (r *screenRaster) setColor(c rgb) {
	r.cr.SetSourceRGB(c.r, c.g, c.b)
}

func (r *screenRaster) background(tone ScreenTone) {
	g := tone.gradient()
	rad := g.angle * math.Pi / 180
	dx, dy := math.Sin(rad), -math.Cos(rad)
	length := math.Abs(r.refW*dx) + math.Abs(r.refH*dy)
	x0, y0 := r.refW/2-dx*length/2, r.refH/2-dy*length/2

	r.surface.Flush()
	stride := cairo.FormatStrideForWidth(cairo.FORMAT_RGB24, r.w) / 4
	pixels := unsafe.Slice((*uint32)(r.surface.GetData()), stride*r.h)
	for y := range r.h {
		rowPos := ((float64(y)+0.5)/r.scale - y0) * dy
		row := pixels[y*stride : y*stride+r.w]
		for x := range row {
			pos := (((float64(x)+0.5)/r.scale-x0)*dx + rowPos) / length
			ramp := max(0, min(1, (pos-g.from)/(g.to-g.from)))
			step := math.Floor((ramp-0.5)*g.span + screenDither[y&1][x&1])
			row[x] = uint32(int32(g.tint) + int32(step)*0x010101)
		}
	}
	r.surface.MarkDirty()
}

func (r *screenRaster) fillPaths(paths []string, rule cairo.FillRule, c rgb, x, y, scale float64) {
	r.cr.Save()
	r.cr.Translate(x, y)
	r.cr.Scale(scale, scale)
	r.cr.SetFillRule(rule)
	r.setColor(c)
	for _, d := range paths {
		r.cr.NewPath()
		traceSVGPath(r.cr, d)
		r.cr.Fill()
	}
	r.cr.Restore()
}

func (r *screenRaster) layoutText(st textStyle, text string) (*pango.Layout, float64, float64) {
	desc := r.regular
	switch st.weight {
	case pango.WEIGHT_MEDIUM:
		desc = r.medium
	case pango.WEIGHT_BOLD:
		desc = r.bold
	}
	desc.SetAbsoluteSize(st.px * screenTextScale * float64(pango.SCALE))
	r.layout.SetFontDescription(desc)
	r.layout.SetText(text, -1)
	w, h := r.layout.GetSize()
	unit := screenTextScale * float64(pango.SCALE)
	return r.layout, float64(w) / unit, float64(h) / unit
}

func (r *screenRaster) showLayout(layout *pango.Layout, c rgb, x, y float64) {
	r.cr.Save()
	r.setColor(c)
	r.cr.Translate(x, y)
	r.cr.Scale(1/screenTextScale, 1/screenTextScale)
	r.cr.MoveTo(0, 0)
	pango.CairoShowLayout(r.cr, layout)
	r.cr.Restore()
}

func (r *screenRaster) textWidth(st textStyle, text string) float64 {
	_, w, _ := r.layoutText(st, text)
	return w
}

func (r *screenRaster) wrap(st textStyle, text string, maxW float64) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if candidate := line + " " + word; r.textWidth(st, candidate) > maxW {
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

func (r *screenRaster) balancedLines(st textStyle, text string, maxW float64) []string {
	lines := r.wrap(st, text, maxW)
	for w := maxW - screenBalanceStep; w > 0; w -= screenBalanceStep {
		narrower := r.wrap(st, text, w)
		if len(narrower) > len(lines) {
			break
		}
		lines = narrower
	}
	return lines
}

func (r *screenRaster) textBox(st textStyle, text string, c rgb, cx, y float64) {
	layout, w, h := r.layoutText(st, text)
	r.showLayout(layout, c, cx-w/2, y+(st.line-h)/2)
}

func (r *screenRaster) textLines(st textStyle, lines []string, c rgb, cx, y float64) {
	for i, line := range lines {
		r.textBox(st, line, c, cx, y+float64(i)*st.line)
	}
}

func (r *screenRaster) entryColumn(s *Screen) {
	cx := r.refW / 2
	y := (r.refH - screenEntryColumnH) / 2
	r.textBox(styleEyebrow, s.Eyebrow, s.Tone.color(), cx, y)
	y += styleEyebrow.line + screenGapSm
	r.textBox(styleTitle, s.Title, screenInk, cx, y)
	y += styleTitle.line + screenGapLg
	r.digits(s.Digits, cx, y)
	y += screenDigitH + screenGapLg
	if s.Body != "" {
		r.textLines(styleBody, r.balancedLines(styleBody, s.Body, screenBodyMaxW), screenTertiary, cx, y)
	}
}

func (r *screenRaster) statusColumn(s *Screen) {
	cx := r.refW / 2
	titleLines := r.wrap(styleTitle, s.Title, r.refW-2*screenLogoX)
	var bodyLines []string
	if s.Body != "" {
		bodyLines = r.balancedLines(styleBody, s.Body, screenBodyMaxW)
	}
	total := float64(len(titleLines)) * styleTitle.line
	if s.Icon != IconNone {
		total += screenIconSize + screenGapSm
	}
	if len(bodyLines) > 0 {
		total += screenGapSm + float64(len(bodyLines))*styleBody.line
	}
	y := (r.refH - total) / 2
	if s.Icon != IconNone {
		r.fillPaths(iconPaths[s.Icon], cairo.FILL_RULE_EVEN_ODD, s.Tone.color(), cx-screenIconSize/2, y, 1)
		y += screenIconSize + screenGapSm
	}
	r.textLines(styleTitle, titleLines, screenInk, cx, y)
	y += float64(len(titleLines)) * styleTitle.line
	if len(bodyLines) > 0 {
		r.textLines(styleBody, bodyLines, s.Tone.color(), cx, y+screenGapSm)
	}
}

func (r *screenRaster) spinnerColumn(s *Screen) {
	cx := r.refW / 2
	y := (r.refH - screenSpinnerSize - screenGapSm - styleCaption.line) / 2
	r.spinnerX, r.spinnerY = cx, y+screenSpinnerSize/2
	r.cr.NewPath()
	r.cr.Arc(r.spinnerX, r.spinnerY, screenSpinnerRadius, 0, 2*math.Pi)
	r.cr.SetLineWidth(screenSpinnerStroke)
	r.setColor(screenSpinnerBack)
	r.cr.Stroke()
	r.textBox(styleCaption, s.Caption, screenSecondary, cx, y+screenSpinnerSize+screenGapSm)
}

func (r *screenRaster) digits(d *ScreenDigits, cx, y float64) {
	groups := digitGroups(d.Length)
	total := float64(d.Length+1)*screenDigitW + float64(d.Length-len(groups))*screenDigitGap + float64(len(groups))*screenDigitGroupGap
	x := cx - total/2
	idx := 0
	for _, g := range groups {
		for k := 0; k < g; k++ {
			ink, line := screenInk, screenDisabled
			switch {
			case d.Error:
				ink, line = screenErrorInk, screenError
			case idx < len(d.Entered):
				line = screenInk
			case idx == len(d.Entered):
				line = screenBrand
			}
			digit := ""
			if idx < len(d.Entered) {
				digit = d.Entered[idx : idx+1]
			}
			r.digitSlot(digit, ink, line, x, y)
			x += screenDigitW + screenDigitGap
			idx++
		}
		x += screenDigitGroupGap - screenDigitGap
	}
	switch {
	case d.Error:
		r.digitSlot("#", screenErrorInk, screenError, x, y)
	case len(d.Entered) >= d.Length:
		r.digitSlot("#", screenBrand, screenBrand, x, y)
	default:
		r.digitSlot("#", screenDisabled, screenDisabled, x, y)
	}
}

func (r *screenRaster) digitSlot(text string, ink, line rgb, x, y float64) {
	if text != "" {
		r.textBox(styleTitle, text, ink, x+screenDigitW/2, y)
	}
	r.cr.Save()
	r.cr.NewPath()
	r.cr.MoveTo(x, y+screenDigitH)
	r.cr.LineTo(x+screenDigitW, y+screenDigitH)
	r.cr.SetLineWidth(screenDigitStroke)
	r.cr.SetLineCap(cairo.LINE_CAP_ROUND)
	r.setColor(line)
	r.cr.Stroke()
	r.cr.Restore()
}

func (r *screenRaster) footer(hints []ScreenHint) {
	widths := make([]float64, len(hints))
	total := screenFooterGap * float64(len(hints)-1)
	for i, h := range hints {
		widths[i] = r.textWidth(styleHint, h.Label)
		if h.Key != "" {
			widths[i] += screenKeySize + screenKeyGap
		}
		total += widths[i]
	}
	x := (r.refW - total) / 2
	y := r.refH - screenFooterBottom - screenKeySize
	for i, h := range hints {
		labelX := x
		if h.Key != "" {
			r.keycap(h.Key, x, y)
			labelX += screenKeySize + screenKeyGap
		}
		layout, _, th := r.layoutText(styleHint, h.Label)
		r.showLayout(layout, screenTertiary, labelX, y+(screenKeySize-th)/2)
		x += widths[i] + screenFooterGap
	}
}

func (r *screenRaster) keycap(key string, x, y float64) {
	roundedRectPath(r.cr, x+0.5, y+0.5, screenKeySize-1, screenKeySize-1, screenKeyRadius)
	r.cr.SetSourceRGBA(screenKeyFill.r, screenKeyFill.g, screenKeyFill.b, screenKeyFillAlpha)
	r.cr.FillPreserve()
	r.cr.SetLineWidth(1)
	r.setColor(screenKeyBorder)
	r.cr.Stroke()
	r.textBox(styleKey, key, screenInk, x+screenKeySize/2, y+(screenKeySize-styleKey.line)/2)
}

func roundedRectPath(cr *cairo.Context, x, y, w, h, r float64) {
	if maxR := math.Min(w, h) / 2; r > maxR {
		r = maxR
	}
	cr.NewPath()
	cr.MoveTo(x+w-r, y)
	cr.Arc(x+w-r, y+r, r, -math.Pi/2, 0)
	cr.Arc(x+w-r, y+h-r, r, 0, math.Pi/2)
	cr.Arc(x+r, y+h-r, r, math.Pi/2, math.Pi)
	cr.Arc(x+r, y+r, r, math.Pi, 3*math.Pi/2)
	cr.ClosePath()
}
