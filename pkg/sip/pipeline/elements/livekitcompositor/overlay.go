package livekitcompositor

import (
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/sip/pkg/i18n"
	"github.com/livekit/sip/res"
	"github.com/vopenia-io/go-pangocairo/cairo"
	"github.com/vopenia-io/go-pangocairo/pango"
	"golang.org/x/sys/unix"
)

//go:embed assets/mute-icon.png
var assets embed.FS

const (
	muteIconSize                 = 20.0
	muteIconBgPad                = 4.0
	muteIconBoxSize              = muteIconSize + 2*muteIconBgPad // outer red rounded-rect size
	bgColorR, bgColorG, bgColorB = 0.085327, 0.084778, 0.11987
	fgColorR, fgColorG, fgColorB = 0.84082, 0.13257, 0.13586
)

var muteIconSurface *cairo.Surface

func init() {
	data, err := assets.ReadFile("assets/mute-icon.png")
	if err != nil {
		panic(fmt.Sprintf("Failed to read embedded mute icon: %v", err))
	}

	fd, err := res.MemfdFromBytes("assets/mute-icon.png", data)
	if err != nil {
		panic(fmt.Sprintf("Failed to create memfd for mute icon: %v", err))
	}
	defer unix.Close(fd)

	surface, err := cairo.NewSurfaceFromPNG(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		panic(fmt.Sprintf("Failed to create cairo surface for mute icon: %v", err))
	}
	surface, err = resizeSurface(surface, muteIconSize, muteIconSize)
	if err != nil {
		panic(fmt.Sprintf("Failed to resize mute icon surface: %v", err))
	}
	muteIconSurface = surface
}

func resizeSurface(s *cairo.Surface, width, height int) (*cairo.Surface, error) {
	if s == nil {
		return nil, fmt.Errorf("cannot resize nil surface")
	}
	dst := cairo.CreateImageSurface(cairo.FORMAT_ARGB32, width, height)
	cr := cairo.Create(dst)
	cr.Scale(float64(width)/float64(s.GetWidth()), float64(height)/float64(s.GetHeight()))
	cr.SetSourceSurface(s, 0, 0)
	cr.Paint()
	return dst, nil
}

type overlayCache struct {
	infos            []participantOverlayInfo
	vW               int
	vH               int
	nTracks          int
	ParticipantCount int
	message          overlayMessage
	fontScale        float64
}

type participantOverlayInfo struct {
	sid        string
	name       string
	identity   string
	muted      bool
	noCamera   bool
	audioLevel float64
}

// should be called with e.mu held
func (e *LivekitCompositor) refreshOverlayCache() {
	if e.LivekitCompositorCamera == nil {
		return
	}
	e.LivekitCompositorCamera.ticker.Stop()
	defer e.LivekitCompositorCamera.ticker.Reset(100 * time.Millisecond)
	cache := &overlayCache{
		infos:            e.collectParticipantOverlayInfo(),
		vW:               int(e.videoWidth),
		vH:               int(e.videoHeight),
		nTracks:          len(e.currentLayout),
		ParticipantCount: len(e.participants),
		message:          e.overlayMessage,
		fontScale:        float64(e.videoWidth) / float64(1280) * float64(pango.SCALE),
	}

	e.LivekitCompositorCamera.overlayCache.Store(cache)
}

// AvatarColor returns a deterministic #RRGGBB hex color for the given name.
// Saturation and lightness are fixed so the result always looks decent.
func AvatarColor(name string) (uint8, uint8, uint8) {
	h := fnv.New32a()
	h.Write([]byte(name))
	hue := float64(h.Sum32() % 360)

	r, g, b := hslToRGB(hue, 0.65, 0.50) // tweak S/L to taste
	return r, g, b
}

// hslToRGB: h in [0,360), s and l in [0,1]. Returns 0–255 RGB.
func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255)
}

func (e *LivekitCompositor) collectParticipantOverlayInfo() []participantOverlayInfo {
	out := make([]participantOverlayInfo, len(e.currentLayout))
	for i, sid := range e.currentLayout {
		p := e.participants[sid]
		name := p.Name
		identity := p.Identity
		if name == "" {
			if identity != "" {
				name = identity
			} else {
				name = "Unknown"
			}
		}
		_, hasMic := e.tracks[livekit.TrackSource_MICROPHONE][sid]
		_, hasCam := e.tracks[livekit.TrackSource_CAMERA][sid]
		level := 0.0
		if hasMic {
			level = float64(p.Level)
		}
		// fmt.Printf("Collecting overlay info for participant %s: name=%s, hasMic=%v, hasCam=%v, level=%.2f\n", sid, name, hasMic, hasCam, level)
		out[i] = participantOverlayInfo{
			sid:        sid,
			name:       name,
			muted:      !hasMic,
			noCamera:   !hasCam,
			audioLevel: level,
			identity:   identity,
		}
	}
	return out
}

func (e *LivekitCompositor) drawOverlayNoTracks(self *gst.Bin, cr *cairo.Context, cache *overlayCache) {
	cr.Save()
	cr.SetSourceRGBA(bgColorR, bgColorG, bgColorB, 1.0)
	cr.Rectangle(0, 0, float64(e.videoWidth), float64(e.videoHeight))
	cr.Fill()
	cr.Restore()

	layout := pango.CairoCreateLayout(cr)
	desc := pango.FontDescriptionFromString("Sans Bold")
	desc.SetSize(int(cache.fontScale * 14))
	layout.SetFontDescription(desc)
	layout.SetText(i18n.Printer(e.lang).Sprintf("You are the only participant..."), -1)
	pw, ph := layout.GetSize()
	w := float64(pw) / float64(pango.SCALE)
	h := float64(ph) / float64(pango.SCALE)

	cr.Save()
	cr.SetSourceRGBA(1, 1, 1, 1)
	cr.MoveTo(float64(e.videoWidth)/2-w/2, float64(e.videoHeight)/2-h/2)
	pango.CairoShowLayout(cr, layout)
	cr.Restore()

	if status := cr.Status(); status != cairo.STATUS_SUCCESS {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("cairo context in error state after drawing no-tracks overlay: %d(%v)", int(status), status))
	}
}

func (e *LivekitCompositor) drawOverlayMessage(self *gst.Bin, cr *cairo.Context, cache *overlayCache) {
	cr.Save()
	cr.SetSourceRGBA(bgColorR, bgColorG, bgColorB, 1.0)
	cr.Rectangle(0, 0, float64(e.videoWidth), float64(e.videoHeight))
	cr.Fill()
	cr.Restore()

	layout := pango.CairoCreateLayout(cr)
	desc := pango.FontDescriptionFromString("Sans Bold")
	desc.SetSize(int(cache.fontScale * 14))
	layout.SetFontDescription(desc)
	layout.SetText(cache.message.Message, -1)
	pw, ph := layout.GetSize()
	w := float64(pw) / float64(pango.SCALE)
	h := float64(ph) / float64(pango.SCALE)

	cr.Save()
	cr.SetSourceRGBA(1, 1, 1, 1)
	cr.MoveTo(float64(e.videoWidth)/2-w/2, float64(e.videoHeight)/2-h/2)
	pango.CairoShowLayout(cr, layout)
	cr.Restore()

	if status := cr.Status(); status != cairo.STATUS_SUCCESS {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("cairo context in error state after drawing no-tracks overlay: %d(%v)", int(status), status))
	}
}

func (e *LivekitCompositor) cameraOverlayDrawCallback(self *gst.Bin, overlay *gst.Element, cr *cairo.Context, timestamp gst.ClockTime) {
	cache := e.LivekitCompositorCamera.overlayCache.Load()
	if cache == nil {
		e.mu.Lock()
		e.refreshOverlayCache()
		cache = e.LivekitCompositorCamera.overlayCache.Load()
		e.mu.Unlock()
		if cache == nil {
			self.Log(CAT, gst.LevelError, "Overlay cache still nil after refresh")
			self.Error("Internal error: overlay cache not available", errors.New("overlay cache not available"))
		}
	}

	if cache.message.Show {
		e.drawOverlayMessage(self, cr, cache)
		return
	}

	infos := cache.infos
	vW := cache.vW
	vH := cache.vH
	nTracks := cache.nTracks

	if nTracks == 0 {
		e.drawOverlayNoTracks(self, cr, cache)
		return
	}

	videoW := float64(vW)
	videoH := float64(vH)

	// path_rounded_rect: build a rounded-rect path at (x,y,w,h) with corner
	// radius r, clamped so the four arcs don't overlap.
	pathRoundedRect := func(x, y, w, h, r float64) {
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

	// pathParticipantRect: rounded rect that frames participant idx's tile.
	const cornerRadius = 8.0
	pathParticipantRect := func(idx int) {
		w, h, x, y := cameraComputeSize(vW, vH, idx, nTracks)
		pathRoundedRect(float64(x), float64(y), float64(w), float64(h), cornerRadius)
	}

	// 1. Mask everything outside the rounded tile interiors with black,
	// so the underlying compositor video is only visible inside the rounded
	// rects. One even-odd fill: outer frame rect XOR'd against N tile holes.
	cr.Save()
	cr.SetFillRule(cairo.FILL_RULE_EVEN_ODD)
	cr.SetSourceRGBA(bgColorR, bgColorG, bgColorB, 1.0)
	cr.Rectangle(0, 0, videoW, videoH)
	for idx := range infos {
		pathParticipantRect(idx)
	}
	cr.Fill()
	cr.Restore()

	// 2. Per-tile decoration.
	drawAvatar := func(cx, cy, radius float64, name, initial string) {
		// Flat-filled disc, color derived from the participant name.
		rByte, gByte, bByte := AvatarColor(name)

		cr.Save()
		cr.SetSourceRGBA(float64(rByte)/255, float64(gByte)/255, float64(bByte)/255, 1.0)
		cr.Arc(cx, cy, radius, 0, 2*math.Pi)
		cr.Fill()
		cr.Restore()

		// Bold initial centered on the disc.
		layout := pango.CairoCreateLayout(cr)
		desc := pango.FontDescriptionFromString("Sans")
		desc.SetSize(int(radius * 0.75 * float64(pango.SCALE)))
		layout.SetFontDescription(desc)
		layout.SetText(initial, -1)
		pw, ph := layout.GetSize()
		w := float64(pw) / float64(pango.SCALE)
		h := float64(ph) / float64(pango.SCALE)

		cr.Save()
		cr.SetSourceRGBA(1, 1, 1, 1)
		cr.MoveTo(cx-w/2, cy-h/2)
		pango.CairoShowLayout(cr, layout)
		cr.Restore()
	}

	const labelPadX, labelPadY = 10.0, 4.0

	// buildPillLayout sizes a pango layout so its logical height fits the
	// pill's inner text area (muteIconBoxSize - 2*labelPadY). Returns the
	// layout plus its measured text width and height.
	buildPillLayout := func(text string) (*pango.Layout, float64, float64) {
		const height = muteIconBoxSize
		targetTextH := height - 2*labelPadY

		layout := pango.CairoCreateLayout(cr)
		desc := pango.FontDescriptionFromString("Sans")
		// Measure at a reference size, then rescale so the layout's logical
		// height equals targetTextH. SetFontDescription copies the desc, so
		// we have to call it again after mutating the absolute size.
		const refPx = 16.0
		desc.SetAbsoluteSize(refPx * float64(pango.SCALE))
		layout.SetFontDescription(desc)
		layout.SetText(text, -1)
		_, ph := layout.GetSize()
		measuredH := float64(ph) / float64(pango.SCALE)
		desc.SetAbsoluteSize(refPx * targetTextH / measuredH * float64(pango.SCALE))
		layout.SetFontDescription(desc)

		pw, ph := layout.GetSize()
		return layout, float64(pw) / float64(pango.SCALE), float64(ph) / float64(pango.SCALE)
	}

	// drawPillAt renders the pre-measured layout into a dark rounded pill
	// with top-left at (x, y).
	drawPillAt := func(layout *pango.Layout, x, y, textW, textH float64) {
		const height = muteIconBoxSize
		cr.Save()
		cr.SetSourceRGBA(0, 0, 0, 0.55)
		pathRoundedRect(x, y, textW+2*labelPadX, height, 6)
		cr.Fill()
		cr.SetSourceRGBA(1, 1, 1, 1)
		cr.MoveTo(x+labelPadX, y+(height-textH)/2)
		pango.CairoShowLayout(cr, layout)
		cr.Restore()
	}

	drawLabelPill := func(text string, x, y float64) {
		layout, w, h := buildPillLayout(text)
		drawPillAt(layout, x, y, w, h)
	}

	drawMuteIcon := func(cx, cy float64) {
		const borderRadius = 4.0
		iw := float64(muteIconSurface.GetWidth())
		ih := float64(muteIconSurface.GetHeight())
		cr.Save()
		cr.SetSourceRGBA(0.84082, 0.13257, 0.13586, 1.0)
		pathRoundedRect(cx-iw/2-muteIconBgPad, cy-ih/2-muteIconBgPad, iw+2*muteIconBgPad, ih+2*muteIconBgPad, borderRadius)
		cr.Fill()
		cr.SetSourceSurface(muteIconSurface, cx-iw/2, cy-ih/2)
		cr.Paint()
		cr.Restore()
	}

	for idx, info := range infos {
		w, h, tx, ty := cameraComputeSize(vW, vH, idx, nTracks)
		x := float64(tx)
		y := float64(ty)
		tw := float64(w)
		th := float64(h)

		// Camera-off: big avatar disc with bold initial.
		if info.noCamera {
			cr.Save()
			cr.SetSourceRGBA(0.15613, 0.15466, 0.24084, 1.0)
			pathRoundedRect(x, y, tw, th, cornerRadius)
			cr.Fill()
			cr.Restore()
			r := math.Min(tw, th) * 0.28
			initial := "?"
			if len(info.name) > 0 {
				initial = strings.ToUpper(string([]rune(info.name)[0]))
			}
			drawAvatar(x+tw/2, y+th/2, r, info.name, initial)
		}

		// Tile outline (drawn AFTER mask so the stroke straddles the
		// boundary correctly — half over the black, half over the video).
		cr.Save()
		cr.SetSourceRGBA(0.26587, 0.54004, 0.94434, 0.9)
		outlineWidth := min(4, info.audioLevel*20)
		// fmt.Printf("Drawing outline for participant %s with audio level %.2f: width=%.2f\n", info.name, info.audioLevel, outlineWidth)
		cr.SetLineWidth(outlineWidth)
		pathRoundedRect(x, y, tw, th, cornerRadius)
		cr.Stroke()
		cr.Restore()

		// Mute icon center; its drawn box spans muteIconBoxSize × muteIconBoxSize
		// around that center, so its top-left lands at (iconLeft, iconTop).
		const tileInset = 8.0
		iconCenterX := x + tileInset + muteIconBoxSize/2
		iconCenterY := y + th - tileInset - muteIconBoxSize/2
		iconTop := iconCenterY - muteIconBoxSize/2
		iconRight := iconCenterX + muteIconBoxSize/2

		pillX := x + tileInset
		if info.muted {
			drawMuteIcon(iconCenterX, iconCenterY)
			pillX = iconRight + 4 // small gap between icon box and pill
		}
		drawLabelPill(info.name, pillX, iconTop)
	}

	// Overflow indicator: when more participants exist than displayed tiles,
	// show a "+N more" pill in the bottom-right of the frame so users know
	// there are off-screen participants (active-speaker rotation only shows
	// up to 6 tiles).
	if hidden := cache.ParticipantCount - nTracks; hidden > 0 {
		text := fmt.Sprintf("+%d more", hidden)
		layout, textW, textH := buildPillLayout(text)
		const margin = 12.0
		pillW := textW + 2*labelPadX
		drawPillAt(layout, videoW-pillW-margin, videoH-muteIconBoxSize-margin, textW, textH)
	}

	if status := cr.Status(); status != cairo.STATUS_SUCCESS {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("cairo context in error state after camera overlay draw: %d(%v)", int(status), status))
	}
}
