# ARFL — Design System

> **Privacy in the Dark Cloud.**
> ARFL is a decentralised privacy protocol that lets anyone browse the internet privately — no account, no subscription, no company to trust. This directory is the single source of truth for its visual + verbal identity: colors, type, textures, assets, and high-fidelity UI kits for prototyping.

Imported from the Claude Design project **"ARFL Design System"** (`https://claude.ai/design/p/6bf8b225-2d60-4fae-a177-4f8fbec932d5`). This `web/` directory mirrors that project's structure so relative paths (e.g. `../../colors_and_type.css`) work unmodified.

---

## What ARFL means

**ARFL** comes from **Araphel (עֲרָפֶל)** — an ancient concept of a thick, protective dark cloud. In the Hebrew tradition, Araphel is not darkness as a hiding place for bad actors. It is darkness as a *sanctuary*: a dense, impenetrable cloud through which only the worthy may pass.

The brand reclaims this: privacy is not the behaviour of criminals. It is the sovereign right of every person to decide who has access to their data. If someone does not have explicit permission, they are locked out. Not because you have something to hide — but because not everyone is worthy of access to what is yours.

This etymology runs through every visual and verbal choice:
- The slogan **"Privacy in the Dark Cloud"** is a direct translation of Araphel's nature.
- The Japanese accent **雲隠れ** (kumogakure — "hidden in the clouds") is a parallel tradition from a different culture, chosen because it rhymes with the same concept.
- The imagery direction — figures concealed in neon darkness — is the same idea made visual.
- The pitch-black canvas is not emptiness. It is the cloud.

ARFL is a **decentralised privacy protocol for the internet itself.** It combines:

- **WireGuard** — encrypted transport.
- **Nostr** — decentralised node discovery.
- **Bitcoin Lightning** — per-bundle micropayments.

…into a network where **system operators earn Bitcoin for providing bandwidth**, and **users pay only for what they use** — no account, no subscription, no identity.

### Brand world
The identity is **cyberpunk / dystopian-machine**: recoloured mecha & neo-Tokyo imagery, katakana micro-accents, wireframe-globe network overlays, photocopy/halftone grain, and a chunky angular cut-corner display face.

---

## Repository index

| Path | What it is |
|---|---|
| `README.md` | This file. |
| `colors_and_type.css` | **Start here.** All colour + type tokens, semantic vars, `@font-face`, texture utilities. |
| `fonts/` | KARIXBY font files + README; `@font-face` auto-loads them. |
| `tweaks-panel.jsx` | Shared floating tweak-panel toolkit used by both UI kits for live design tuning. |
| `assets/imagery/` | Recoloured cyberpunk reference imagery used as backgrounds/section art. |
| `assets/mascot/` | Cinematic cyberpunk figure used as full-bleed brand art. |
| `ui_kits/website/` | Marketing site UI kit (hero, protocol features, manifesto, footer). |
| `ui_kits/app/` | Private-connection client UI kit (connect, exit nodes, usage, earn). |

---

## Colour palette (founder-defined core)

| Token | Hex | Name | Meaning |
|---|---|---|---|
| `--pitch-black` | `#0D0D0D` | Pitch Black | Canvas |
| `--midnight-charcoal` | `#1A1A24` | Midnight Charcoal | Depth |
| `--blood-red` | `#B30F0F` | Blood Red | Power. Impact. Warning. |
| `--cosmic-gold` | `#E6B800` | Cosmic Gold | Value. Bitcoin. Light. |
| `--neon-cyan` | `#00E5FF` | Neon Cyan | Technology. Lightning. Signal. |
| `--muted-lavender` | `#9D7BB0` | Muted Lavender | Text. Subtlety. Calm. |

Extended ramps in `colors_and_type.css` are oklch-harmonised derivatives for hover/press/surfaces. **Never invent new hues** outside these families.

Colour hierarchy is three strict tiers: **Leads** (cyan — primary CTAs, live status), **Accents** (gold — value/Bitcoin/earn), **Warns** (red — danger/destructive only), **Default** (lavender — text, borders, neutral states).

---

## Type

- **Display — KARIXBY** (substitute: Chakra Petch). Chunky, angular, cut-corner, blocky, all-caps. Headlines + wordmark.
- **UI — Space Grotesk.** Technical grotesque, tight tracking.
- **Body — IBM Plex Sans.** Neutral, legible.
- **Mono — Space Mono.** Addresses, hashes, node IDs, throughput.

---

## Shape language

**Variant B asymmetric chamfer** — top-right + bottom-left corners cut via CSS `clip-path: polygon(...)`, implemented by the `cc()` helper (website kit) and `appCC()` helper (app kit). Applied to buttons, cards, and containers throughout both kits.

Texture is always-on: procedural photocopy/halftone/paper grain (`.grain-over`, `--grain-url`, `.halftone`) layered over every surface at low opacity.

---

## Known gaps — placeholder assets

The Claude Design MCP's `get_file` method caps reads at 256 KiB. Three binary image assets in the source project exceed that cap and came back truncated, so they could **not** be transferred byte-for-byte into this repo:

- `assets/imagery/cyber-portrait.png`
- `assets/imagery/network-globe.png`
- `assets/mascot/satoshi.png`

The files at those paths in this repo are **honest placeholders** — plain dark-gradient art, clearly not the real moodboard-derived imagery described in this README (recoloured mecha/cyberpunk figures, a wireframe network globe, a hooded cyberpunk-anime figure). They exist so the UI kits render without broken images, nothing more.

To replace them with the real assets, pull the originals from the source Claude Design project at `https://claude.ai/design/p/6bf8b225-2d60-4fae-a177-4f8fbec932d5` (paths above) and drop them in at the same relative paths — no code changes needed.

All other files in this directory (CSS, JSX, HTML, the KARIXBY demo font) were transferred in full.

---

## Running the kits

Both UI kits are dependency-free React 18 + Babel-standalone + Lucide prototypes loaded entirely via CDN — no build step. Open `ui_kits/website/index.html` or `ui_kits/app/index.html` directly in a browser, or serve `web/` with any static file server.
