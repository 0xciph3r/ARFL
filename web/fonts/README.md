# fonts/ — drop the real KARIXBY files here

The ARFL display face is **KARIXBY** by Fachmy Casofa (ENXYCLO Studio / SOC-IDN) — a cyberpunk / mecha display typeface. It is **commercial** and is *not* bundled here. `colors_and_type.css` already has `@font-face` rules pointing at this folder, so the brand "lights up" the instant you add the licensed files.

## Expected filenames
Drop any/all of these (woff2 preferred; ttf/otf also work):

| Style | Filenames the CSS looks for |
|---|---|
| Regular | `KARIXBY-Regular.woff2` · `.woff` · `.ttf` |
| Slant   | `KARIXBY-Slant.woff2` · `.woff` · `.ttf` |
| Outline | `KARIXBY-Outline.woff2` · `.woff` · `.ttf` |

If your files are named differently (e.g. `Karixby-Italic.otf`), either rename them to match, or update the `@font-face src` paths at the top of `colors_and_type.css`.

## Until then — substitute in use
The font stack falls back to **Chakra Petch** (Google Fonts) — chosen for its chamfered cut-corner techno character, the closest free match to KARIXBY's signature. It is lighter and less chunky than the real face. **This is a flagged substitute.** Everything will reflow correctly once the real files land — no other edits needed.

## What's already here — and a known problem with it
`KARIXBY-Demo.otf` is checked in — a demo cut of the Regular style (uppercase + numerals + punctuation, no lowercase). It is still the commercial foundry's font, not a free-for-redistribution release — treat it the same as any other licensed asset in this repo and replace it with the fully-licensed Regular/Slant/Outline files before shipping a public build.

**This specific file is corrupted and does not render in any browser.** Verified with `fonttools` and Google's own `ots-sanitize` (the same OpenType sanitizer Chromium and Firefox run on every embedded font before use):

- The `GPOS` table's declared length overruns the end of the file by 127 bytes — the file is truncated.
- Independent of that, the `CFF ` table (the actual glyph outlines) fails to parse ("Failed to parse Top DICT Data") — this is not fixable by trimming the truncated table; the glyph data itself is malformed.

Browsers silently reject a font that fails sanitization and fall through to the next family in the stack — so despite this file being checked in and wired up via `@font-face`, every "KARIXBY" render in this repo is actually falling back to **Chakra Petch**, the documented substitute, not the real face. This was true from the moment the file was pulled from the source Claude Design project; it isn't something introduced later in this repo.

Fix: get a valid re-export of KARIXBY-Demo.otf from the source (Claude Design project or the foundry) and replace this file. Nothing else needs to change — the `@font-face` rule already points here.
