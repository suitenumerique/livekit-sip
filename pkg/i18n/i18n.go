// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package i18n provides localized, user-facing messages.
//
// Messages are written as English string literals at the call site using a
// *message.Printer obtained from Printer, e.g. p.Sprintf("Invalid PIN"). The
// gotext tool (see the go:generate directive below) extracts those literals
// from the packages that use them and, together with the hand-translated
// locales/<lang>/messages.gotext.json files, regenerates catalog.go. The
// generated catalog registers the translations with message.DefaultCatalog at
// init time, so no files are loaded at runtime.
package i18n

//go:generate go run golang.org/x/text/cmd/gotext -srclang=en update -out=catalog.go -lang=en,fr github.com/livekit/sip/pkg/sip github.com/livekit/sip/pkg/sip/pipeline/elements/livekitcompositor

import (
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Printer returns a message.Printer for the given config language (e.g. "en" or
// "fr"). Unknown or empty languages fall back to the source language (English).
func Printer(lang string) *message.Printer {
	tag, err := language.Parse(lang)
	if err != nil {
		tag = language.English
	}
	return message.NewPrinter(tag)
}
