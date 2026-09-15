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

type rgb struct{ r, g, b float64 }

func hexRGB(c uint32) rgb {
	return rgb{float64(c>>16&0xff) / 255, float64(c>>8&0xff) / 255, float64(c&0xff) / 255}
}

// Meet light palette (panda.config.ts tokens).
var (
	screenBg      = hexRGB(0xF5F5FE) // primary.50
	screenWhite   = hexRGB(0xFFFFFF)
	screenLine    = hexRGB(0xCACAFB) // primary.300
	screenAccent  = hexRGB(0x000091) // primary.800
	screenInk     = hexRGB(0x161616) // greyscale.1000
	screenDim     = hexRGB(0x3A3A3A) // greyscale.700
	screenMuted   = hexRGB(0x7C7C7C) // greyscale.500
	screenError   = hexRGB(0xCA3632) // error.400
	screenErrLine = hexRGB(0xEE6A66) // error.600
	screenSuccess = hexRGB(0x15803D) // green.700
)

const (
	screenFontSans = "Atkinson Hyperlegible Next,DejaVu Sans,Sans"
	screenFontMono = "Atkinson Hyperlegible Mono,DejaVu Sans Mono,Monospace"
)

// Layout metrics at the 1280×720 reference size; the painter scales them to the frame.
const (
	screenRefW = 1280.0

	screenMarginX = 64.0
	screenMarginY = 40.0
	screenTopBarH = 44.0
	screenFooterH = 40.0
	screenColumnW = 900.0

	screenIconSize = 88.0
	screenIconGap  = 28.0

	screenEyebrowPx  = 24.0
	screenEyebrowGap = 16.0
	screenTitlePx    = 52.0
	screenTitleLead  = 1.15
	screenBodyPx     = 28.0
	screenBodyLead   = 1.4
	screenBodyGap    = 20.0

	screenFooterPx  = 24.0
	screenFooterGap = 48.0
	screenKeySize   = 40.0
	screenKeyPx     = 22.0
	screenKeyGap    = 12.0
	screenKeyRadius = 6.0

	screenDigitPx       = 60.0
	screenDigitW        = 62.0
	screenDigitH        = 84.0
	screenDigitGap      = 10.0
	screenDigitGroupGap = 34.0
	screenDigitsTop     = 44.0
	screenDigitLine     = 3.0
	screenDigitLineCur  = 4.0
)

func (t ScreenTone) color() rgb {
	switch t {
	case ToneError:
		return screenError
	case ToneSuccess:
		return screenSuccess
	case ToneMuted:
		return screenMuted
	}
	return screenAccent
}

// digitGroups splits n positions the way the invitation prints the code (3·3·4).
func digitGroups(n int) []int {
	if n == 10 {
		return []int{3, 3, 4}
	}
	return []int{n}
}
