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

	"github.com/vopenia-io/go-pangocairo/cairo"
)

// Icons are stroked on a 24-unit grid, line width 1.6, no fill.
const iconGrid = 24.0

func iconCircle(cr *cairo.Context, cx, cy, r float64) {
	cr.MoveTo(cx+r, cy)
	cr.Arc(cx, cy, r, 0, 2*math.Pi)
}

func drawScreenIcon(cr *cairo.Context, icon ScreenIcon, x, y, size float64, c rgb) {
	cr.Save()
	cr.Translate(x, y)
	cr.Scale(size/iconGrid, size/iconGrid)
	cr.SetLineWidth(1.6)
	cr.SetLineCap(cairo.LINE_CAP_ROUND)
	cr.SetLineJoin(cairo.LINE_JOIN_ROUND)
	cr.SetSourceRGB(c.r, c.g, c.b)

	switch icon {
	case IconHourglass:
		cr.MoveTo(7, 3)
		cr.LineTo(17, 3)
		cr.MoveTo(7, 21)
		cr.LineTo(17, 21)
		cr.MoveTo(8, 3.5)
		cr.CurveTo(8, 8.5, 12, 9.5, 12, 12)
		cr.CurveTo(12, 14.5, 8, 15.5, 8, 20.5)
		cr.MoveTo(16, 3.5)
		cr.CurveTo(16, 8.5, 12, 9.5, 12, 12)
		cr.CurveTo(12, 14.5, 16, 15.5, 16, 20.5)
	case IconPerson:
		iconCircle(cr, 12, 8, 3.5)
		cr.MoveTo(5, 20)
		cr.CurveTo(6, 16, 9, 14, 12, 14)
		cr.CurveTo(15, 14, 18, 16, 19, 20)
	case IconCheck:
		iconCircle(cr, 12, 12, 9)
		cr.MoveTo(7.5, 12.5)
		cr.LineTo(10.5, 15.5)
		cr.LineTo(16.5, 9)
	case IconCross:
		iconCircle(cr, 12, 12, 9)
		cr.MoveTo(8.5, 8.5)
		cr.LineTo(15.5, 15.5)
		cr.MoveTo(15.5, 8.5)
		cr.LineTo(8.5, 15.5)
	case IconClock:
		iconCircle(cr, 12, 12, 9)
		cr.MoveTo(12, 7)
		cr.LineTo(12, 12)
		cr.LineTo(15.5, 14)
	case IconPeople:
		iconCircle(cr, 9, 8, 3)
		iconCircle(cr, 17, 9, 2.5)
		cr.MoveTo(2.5, 20)
		cr.CurveTo(3.3, 16.5, 5.8, 14.5, 9, 14.5)
		cr.CurveTo(12.2, 14.5, 14.7, 16.5, 15.5, 20)
		cr.MoveTo(15.5, 14.8)
		cr.CurveTo(18.3, 14.8, 20.5, 16.4, 21.3, 19.5)
	}
	cr.Stroke()
	cr.Restore()
}
