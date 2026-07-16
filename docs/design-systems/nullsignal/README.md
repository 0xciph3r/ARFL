# NULLSIGNAL — a DeepPCB × NULLCITY hybrid

A "baby" system born from two parents:

- **DeepPCB** (`../deeppcb/`) — the structural parent. Contributes the content-grid
  layout system, the fluid `clamp()` spacing/type scale, and the Space Grotesk /
  Geist / Space Mono type stack.
- **NULLCITY** (Behance: *NULLCITY — Cyberpunk Brand Identity* by Al Mahin &
  Mohammad Babu, plus the two cyberpunk moodboard references supplied) — the
  visual parent. Contributes the obsidian/surgical monochrome base, a single
  neon accent, and a technical/UI vocabulary (bracket tags, index numbers,
  barcode motifs, crosshair corner marks).

Nothing here is templated cyberpunk-by-numbers: no purple-to-blue gradient
hero, no Orbitron/sci-fi display font, no rounded-everything. The accent is
one color, spent once; the rest of the palette is quiet monochrome.

## What each parent gave up

DeepPCB's soft, rounded, orange-on-white SaaS look doesn't survive the
crossing — the 8–24px radii flatten to near-square (2px), the light `--clr-muted`
surfaces go obsidian, and the pill badges become bracketed technical tags.
NULLCITY's raw Photoshop/Illustrator apparel-mockup aesthetic doesn't survive
either — it has no coded layout system, so the content-grid, spacing scale,
and component structure are inherited wholesale from DeepPCB and re-skinned.

## Color

| Token | Value | Heritage | Use |
|---|---|---|---|
| `--clr-void` | `#0a0a0d` | NULLCITY (Obsidian Black) | Base surface — the site is committed to dark, not theme-toggled |
| `--clr-surgical` | `#f2f3f0` | NULLCITY (Surgical White) | Primary text, hairlines on hover |
| `--clr-signal` | `#ff5a1f` | DeepPCB primary, pushed to neon | The one accent: CTAs, active tags, focus ring |
| `--clr-signal-dim` | `#c2430f` | — | Signal hover/active |
| `--clr-cyan` | `#23e5d1` | NULLCITY moodboard signage | Secondary accent — used once or twice, never paired with signal as a gradient |
| `--clr-line` | `#2a2b30` | blend | Hairline borders on dark |
| `--clr-mist` | `#9a9ba3` | blend | Secondary text on dark |
| `--clr-ash` | `#56575e` | blend | Tertiary text, disabled |

## Typography

Same stack as DeepPCB, different register:

- **Space Grotesk**, weight 700, tight tracking, upper-case for display —
  reads as "bold futuristic typography" without reaching for a stereotypical
  sci-fi face.
- **Geist** for body copy — unchanged.
- **Space Mono** promoted from an accent-only role to the voice of every
  technical element: index numbers (`[ 01 ]`), tag brackets (`[ PCB-ROUTING ]`),
  timestamps, barcode labels.

## Layout

The DeepPCB content-grid (`content` 1400px / `breakout` 1600px / `full-width`)
and its fluid spacing scale are inherited unchanged — see
[`../deeppcb/tokens.css`](../deeppcb/tokens.css). NULLSIGNAL only adds the
skin: radius drops to `2px` everywhere, borders replace shadows entirely, and
every card gets a small mono index number and crosshair corner ticks (a
direct nod to NULLCITY's crosshair/barcode UI elements).

## Literal pulls from the two reference images

Two images were supplied directly as inspiration alongside the NULLCITY
Behance write-up: a cyberpunk cityscape (neon kanji/katakana signage, warm
sunset gradient, teal neon glow) and a dark UI mock for a "CYBER" apparel
storefront. Rather than paraphrase them into abstract mood words, specific
devices were lifted and rebuilt as components:

| Reference device | Source image | Component |
|---|---|---|
| Bold wordmark + bracketed katakana gloss (`CYBER [コマース]`) | UI mock | `.wordmark-gloss` — `NULLSIGNAL [ヌルシグナル]` |
| Scrolling ticker band of a repeated word, gold/mustard | UI mock | `.ticker` (static by default; `.ticker--scroll` adds one slow pass, `prefers-reduced-motion`-safe) |
| Small rounded dark ID tags overlaid on product photos (`407`, `80-A45`) | UI mock | `.id-chip` |
| Scuffed print-texture background | UI mock | `.grain-surface` |
| Rotated vertical corner label (`SCROLL [スクロール]`) | UI mock | `.rotated-tag` |
| Glowing vertical kanji/katakana signage strips | Cityscape | `.signage-strip` — set to *meaningful* short words (信号 "signal", 回路 "circuit", 経路 "route") rather than decorative filler, since the source images use real signage text, not noise |
| Warm sunset gradient (orange → magenta → violet) | Cityscape | Reserved for a hero background only, never as a generic button/card gradient — see artifact hero |
| Teal neon glow | Cityscape | `--clr-teal`, used only on `.signage-strip` |

## Components (new/changed from DeepPCB)

- **Bracket tag** — `[ LABEL ]` in Space Mono, replaces the pill `.badge` /
  `.category-badge`. Solid-signal fill for the primary tag, outlined for
  secondary ones.
- **Barcode rule** — a `repeating-linear-gradient` bar (CSS, not hand-drawn
  SVG) used as a section divider / card footer, standing in for NULLCITY's
  barcode graphic.
- **Crosshair card** — the DeepPCB post-card, re-skinned: obsidian surface,
  `--clr-line` hairline, four small `+` corner ticks, mono index number
  top-left instead of a category pill.
- **Signal button** — solid `--clr-signal`, near-square radius, upper-case
  Space Mono label; ghost variant outlines in `--clr-surgical` at low
  opacity.

Full token set: [`tokens.css`](./tokens.css). Rendered reference: see the
published artifact linked from the PR/commit that added this doc.
