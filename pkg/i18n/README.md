# i18n

Localized, user-facing messages, built on
[`golang.org/x/text/message`](https://pkg.go.dev/golang.org/x/text/message) and the
[`gotext`](https://pkg.go.dev/golang.org/x/text/cmd/gotext) extraction tool.

Messages are written as **English string literals at the call site**. The `gotext`
tool scans the source, extracts those literals, and — together with the
hand-translated `locales/<lang>/messages.gotext.json` files — regenerates
`catalog.go`. The generated catalog registers all translations on
`message.DefaultCatalog` at init time, so nothing is loaded from disk at runtime.

The active language comes from the global `lang` config field (`"en"` / `"fr"`,
default `"en"`); unknown or empty languages fall back to English.

## Using it

```go
p := i18n.Printer(conf.Lang) // *message.Printer
p.Sprintf("Invalid PIN")     // -> "Code PIN invalide" when lang == "fr"
```

Write the **English text inline** as the `Sprintf` format string — not via a
`const` or variable, otherwise `gotext` can't extract it. Use `%s`, `%d`, … for
runtime values as usual.

## Regenerating after changing strings

1. Edit/add `p.Sprintf("...")` calls in the source. The `//go:generate` line in
   `i18n.go` lists the packages gotext scans (currently `pkg/sip` and the
   `livekitcompositor` element). If you add localized text in a new package, add
   that package's import path to the directive.
2. Extract:
   ```sh
   go generate ./pkg/i18n/
   ```
   New or changed messages show up as `Missing entry` warnings and land in
   `locales/<lang>/out.gotext.json`.
3. Fill in the `translation` field for each new message in
   `locales/<lang>/messages.gotext.json` (this is the file you edit;
   `out.gotext.json` is regenerated and should not be hand-edited). Keep any
   `{Placeholder}` tokens in the translated text.
4. Regenerate so the translations are compiled into `catalog.go`:
   ```sh
   go generate ./pkg/i18n/
   ```
   A clean run (no `Missing entry` warnings) means every message is translated.

## Adding a language

1. Add the locale to the `-lang=` list in the `//go:generate` directive in
   `i18n.go` (e.g. `-lang=en,fr,es`).
2. Run `go generate ./pkg/i18n/` to create `locales/<lang>/`.
3. Copy `out.gotext.json` to `messages.gotext.json` in that locale, fill in the
   translations, and run `go generate ./pkg/i18n/` again.

## Files

| Path | Hand-edited? | Purpose |
| --- | --- | --- |
| `i18n.go` | yes | `Printer` helper + `go:generate` directive |
| `catalog.go` | no (generated) | compiled translations, registered at init |
| `locales/<lang>/messages.gotext.json` | yes | translation source |
| `locales/<lang>/out.gotext.json` | no (generated) | extraction record |
