# DeepPCB Design System (scraped from deeppcb.ai/blog)

Reverse-engineered from the live markup and stylesheet of `https://deeppcb.ai/blog/`
(theme stylesheet: `wp-content/themes/deeppcb/assets/css/blog.DdgfnAZi.css`).
Source-accurate values only — nothing here is guessed; anything not present in the
scraped CSS is omitted. Machine-readable tokens live in [`tokens.css`](./tokens.css).

## Color

| Token | Value | Use |
|---|---|---|
| `--clr-primary` | `#f4741b` | Brand orange — primary buttons, links, badges, focus ring |
| `--clr-primary-darker` | `#e55a11` | Primary button hover |
| `--clr-light` | `#ffffff` | Base surface, text on dark |
| `--clr-muted` | `#f9fafb` | Card/surface background (post cards, featured post) |
| `--clr-muted-500` | `#7f828d` | Muted footer text, section labels |
| `--clr-dark` | `#1f2937` | Body text color |
| `--clr-Gray-100` | `#f3f4f6` | Hover backgrounds, footer link text |
| `--clr-Gray-200` | `#e5e7eb` | Card/hero borders |
| `--clr-Gray-300` | `#d1d5db` | `btn--light` border |
| `--clr-Gray-400` | `#9ca3af` | Hero note text |
| `--clr-Gray-500` | `#6b7280` | Secondary/excerpt text, hover states |
| `--clr-Gray-600` | `#4b5563` | (defined, reserved) |
| `--clr-Gray-700` | `#374151` | (defined, reserved) |
| `--clr-Gray-800` | `#1f2937` | `btn--light` text |
| `--clr-Gray-900` | `#02050c` | Footer background, wordmark ink |
| — | `#232831` | Dark hairline border (`btn--transparent`, footer `post-footer` divider) |

**Gradients** (used on `.gradient-title`, headline text via `background-clip: text`):
- Default (on light bg): `linear-gradient(90deg, #25150e, #4d2711, #25150e)` — dark warm brown
- `--light` (on dark bg): `linear-gradient(90deg, #ffffff, #ddc2b0)`
- `--primary` (accent): `linear-gradient(90deg, #f4741b, #bd5b18)`
- Featured-image panel: `linear-gradient(180deg, #ffffff 0%, rgba(244,116,27,.08) 100%)`

Selection color (`::selection`) is brand orange with white text.

## Typography

| Role | Font | Weights available |
|---|---|---|
| Headings (`--ff-Heading`) | `"Space_Grotesk", "Arial", sans-serif` | 400, 500, 700 (variable) |
| Body (`--ff-body`) | `"Geist", "Arial", sans-serif` | 400–700 (variable font) |
| Accent/mono (`.rectangle`) | `Space_Mono, monospace` | 400 |

- Base body: `font-size: 1rem` (16px), `line-height: 1.5`, weight 400, color `--clr-dark`.
- `h1, h2, h3`: centered by default, `line-height: 1.125`, `letter-spacing: -2.56px`, weight 500, `text-wrap: balance`.
- Fluid heading scale (CSS `clamp()`, viewport-responsive):
  - `.big-title` → `clamp(2rem, …, 4rem)` (32–64px)
  - `.small-title` → `clamp(1.5rem, …, 2rem)` (24–32px)
  - `.medium-title` → `clamp(1rem, …, 1.5rem)` (16–24px), weight 500
- Body copy uses `text-wrap: pretty` on `p, li, figcaption`.
- Tight tracking throughout: buttons `-0.32px`, nav links `-0.28px`, headings `-2.56px` — a consistently condensed, technical feel.

## Spacing

Two parallel scales, both expressed as CSS custom properties:

1. **Fixed scale** — `--px-4` … `--px-144` (4px → 144px in rem), the base 4px/8px rhythm.
2. **Fluid scale** — dozens of `clamp()` tokens (e.g. `--px-24-48`, `--px-32-64`, `--px-64-128`) that interpolate between a mobile and desktop value across the viewport width, used for section padding, card gaps, and hero spacing so the layout scales continuously rather than jumping at breakpoints.

Section rhythm:
- `.padding-block` → `clamp(9rem, …, 13rem)` (large hero/section vertical padding)
- `.padding-top` / `.padding-bottom` → `clamp(3.5rem, …, 7.5rem)`

## Layout grid

A named-line CSS Grid "content-grid" system (`.content-grid`, `.full-width`) with three nested widths:
- `content` — capped at **1400px**, fluid inline padding `clamp(1rem, …, 16.25rem)`
- `breakout` — capped at **1600px** (for elements that bleed wider than body copy)
- `full-width` — edge to edge

Any direct child is placed in the `content` track by default; add `.breakout` or `.full-width` to opt into the wider tracks. `.row` / `.column` are flex utility classes (`display:flex`, direction, `gap`, `align-items`, `justify-content` all overridable via CSS vars).

## Radius & elevation

| Component | Radius |
|---|---|
| Buttons | `--px-8` (8px) |
| Post cards | 12px (top corners only on the thumbnail) |
| Featured post card | `--px-24` (24px) |
| Hero asset / featured image | `--px-16` (16px) |
| Badges / category pills | `99px` (full pill) |
| Pagination items | 4px |

Shadows are minimal/flat — the only box-shadow is the scrolled header: `0 2px 10px rgba(0,0,0,0.1)` plus a `backdrop-filter: blur(10px)`. Depth otherwise comes from borders (`--clr-Gray-200`) rather than shadows.

Focus state (accessibility): `outline: 1px solid var(--clr-primary); outline-offset: 3px` on `:focus-visible` — visible on every interactive element, not just buttons.

## Components

**Buttons** (`.btn`) — base: no border by default, `border-radius: 8px`, weight 500, `letter-spacing: -0.32px`, cursor pointer.
- `.btn--primary` — solid orange, white text, darker orange on hover.
- `.btn--light` — white bg, `1px solid Gray-300` border, `Gray-800` text, `Gray-100` bg on hover.
- `.btn--transparent` — `1px solid #232831` border, `Gray-100` text, fills `#232831` on hover (used in dark footer).
- Sizes: `--small` (8px 14px), `--medium` (12px 20px), `--large` (14px 24px).

**Badges**
- `.badge` — solid pill, primary bg, white text, uppercase, 14px, used for "Latest Release" tag.
- `.category-badge` — outlined pill, `1px solid currentColor`, 14px, default color `Gray-500`; `.category-badge--link` variant fills solid primary on hover/focus.

**Header** — fixed, transparent over the hero; on scroll (`.header.scrolled`) gains a white background, `backdrop-filter: blur(10px)`, and the `0 2px 10px rgba(0,0,0,.1)` shadow. Nav links 14px/500 weight, `-0.28px` tracking, `Gray-500` on hover. Below 1300px it collapses to a hamburger (`#menu-icon`) driving a full-screen overlay menu.

**Hero** — centered column layout, animated SVG background (`hero_animated.svg`), heading capped at `900px`, description in `Gray-500` at `-0.32px` tracking/weight 500.

**Featured post card** (`#featured-post`) — `Gray-50`-ish (`--clr-muted`) surface, `1px Gray-200` border, `24px` radius, split row layout (content ~45% / image ~55%, stacks on mobile <900px), image panel min-height 320px with the light→primary-tint gradient behind it.

**Article/post card** (`.post`) — `--clr-muted` surface, `1px Gray-200` border, `12px` radius (top-only radius on the thumbnail), `20px` horizontal content padding, `12px` internal gap, title 16–24px fluid/weight 600 with 2-line clamp, excerpt in `Gray-500` also 2-line clamp. Grid: 3 columns desktop → 2 columns (600–1000px) → 1 column (<600px), gap 24px (32px on mobile).

**Pagination** — bordered pill/square buttons (`currentColor` border, 4px radius, min 2.5rem square), current page at full opacity + weight 600, others at 0.85 opacity.

**Footer** — near-black (`--clr-Gray-900` / `#02050c`) surface, white/`Gray-100` text. 5-column link grid (3 columns tablet, 2 mobile), section labels uppercase 14px in `--clr-muted-500`, links 30px line-height in `Gray-100` fading to `--clr-muted-500` on hover. Bottom bar separated by a `1px solid #232831` hairline, disclaimer/copyright text 14px in `--clr-muted-500`.

## Notes for reuse

- The whole system leans on CSS custom properties for both color and — unusually — spacing/typography, including fluid `clamp()` tokens computed per-breakpoint. Porting this system means porting the token set in `tokens.css`, not just the hex values.
- Brand personality: technical/engineering-tool aesthetic — monospace accent font, tight letter-spacing, orange-on-near-black contrast, flat borders over shadows, pill badges for taxonomy.
