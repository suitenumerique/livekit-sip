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

	"github.com/vopenia-io/go-pangocairo/pango"
)

type rgb struct{ r, g, b float64 }

func hexRGB(c uint32) rgb {
	return rgb{float64(c>>16&0xff) / 255, float64(c>>8&0xff) / 255, float64(c&0xff) / 255}
}

var (
	screenBg           = hexRGB(0x181B24)
	screenInk          = hexRGB(0xEEF1F4)
	screenSecondary    = hexRGB(0xDFE2EA)
	screenTertiary     = hexRGB(0xB2B9C7)
	screenBrand        = hexRGB(0xA2B6FF)
	screenError        = hexRGB(0xFF9F99)
	screenErrorInk     = hexRGB(0xFFEDEB)
	screenDisabled     = hexRGB(0x555E74)
	screenKeyFill      = hexRGB(0x2B303D)
	screenKeyBorder    = hexRGB(0x363B4C)
	screenSpinnerBack  = hexRGB(0x3F4759)
	screenSpinnerFront = hexRGB(0x7B8498)
)

const screenFont = "Hanken Grotesk,DejaVu Sans,Sans"

const (
	screenRefW = 960.0
	screenRefH = 540.0

	screenLogoX = 64.0
	screenLogoY = 40.0

	screenGapSm        = 12.0
	screenGapLg        = 32.0
	screenBodyMaxW     = 350.0
	screenEntryColumnH = 212.0
	screenIconSize     = 36.0

	screenDigitW        = 24.0
	screenDigitH        = 42.0
	screenDigitGap      = 8.0
	screenDigitGroupGap = 24.0
	screenDigitStroke   = 2.0

	screenFooterBottom = 44.0
	screenFooterGap    = 24.0
	screenKeySize      = 24.0
	screenKeyGap       = 6.0
	screenKeyRadius    = 4.0
	screenKeyFillAlpha = 0.8

	screenSpinnerSize   = 32.0
	screenSpinnerRadius = 13.0
	screenSpinnerStroke = 2.815
	screenSpinnerStart  = -0.75 * math.Pi
	screenSpinnerSweep  = 0.7 * math.Pi

	screenGradientAlpha = 0.15

	screenTextScale   = 8.0
	screenBalanceStep = 8.0
)

type textStyle struct {
	px     float64
	line   float64
	weight pango.Weight
}

var (
	styleEyebrow = textStyle{14, 18, pango.WEIGHT_MEDIUM}
	styleTitle   = textStyle{32, 40, pango.WEIGHT_BOLD}
	styleCaption = textStyle{16, 22, pango.WEIGHT_MEDIUM}
	styleBody    = textStyle{14, 18, pango.WEIGHT_NORMAL}
	styleHint    = textStyle{14, 18, pango.WEIGHT_MEDIUM}
	styleKey     = textStyle{16, 22, pango.WEIGHT_BOLD}
)

type screenGradient struct {
	angle, from, to float64
	color           rgb
}

func (t ScreenTone) gradient() screenGradient {
	if t == ToneError {
		return screenGradient{49.0573, 0.15616, 1.1174, hexRGB(0xCF202D)}
	}
	return screenGradient{26.5757, 0.2159, 1.0984, hexRGB(0x3E5DE7)}
}

func (t ScreenTone) color() rgb {
	if t == ToneError {
		return screenError
	}
	return screenBrand
}

func digitGroups(n int) []int {
	if n == 10 {
		return []int{3, 3, 4}
	}
	return []int{n}
}

func newFontDesc(weight pango.Weight) *pango.FontDescription {
	desc := pango.FontDescriptionNew()
	desc.SetFamily(screenFont)
	desc.SetWeight(weight)
	return desc
}
