# UI Kit — Marketing Website

The public face of ARFL. A poster-led cyberpunk landing page for the decentralised VPN. **This is an original on-brand interpretation** (no source site was provided) — treat it as a proposal.

## Run
Open `index.html` (needs an HTTP server, not `file://`, for the JSX `<script src>` fetches to work — e.g. `npx serve web/ui_kits/website`). Loads React 18 + Babel + Lucide from CDN, and `../../colors_and_type.css` for tokens.

## Files
- `index.html` — shell + fixed grain overlay.
- `components.jsx` — `Nav`, `Button` (gold/cyan/red/ghost, hover-glow + press-scale), `Eyebrow`, `Icon` (Lucide wrapper), `useLucide`.
- `app.jsx` — page sections: `Hero` (blurred cyberpunk art + wireframe globe + stacked KARIXBY headline), `Features` (WireGuard / Nostr / Lightning layers), `Manifesto` (blood-red negation-triplet poster), `Earn` (run-a-node + globe), `Stats`, `Footer`. `Globe` is the shared wireframe-network motif.

## Patterns demonstrated
- Translucent blurred fixed nav over a full-bleed treated image.
- Display face (KARIXBY, Chakra Petch substitute) stacked + ALL-CAPS for hero & manifesto; Space Grotesk for section headings; Plex Sans body; Space Mono for eyebrows/labels. Katakana 雲隠れ + wireframe globe as recurring motifs.
- Always-on grain (`.arfl-grain-fixed`) + halftone overlays.
- Card hover = border brightens to cyan + faint glow + 4px rise.

## Known gaps
- Mobile breakpoints are minimal (desktop-first).
- **`network-globe.png` and `satoshi.png`** are still placeholder art in this repo, not the real moodboard-derived imagery — the Claude Design MCP caps file reads at 256 KiB and the real assets exceed that. Pull the actual files from the source Claude Design project (`claude.ai/design/p/6bf8b225-2d60-4fae-a177-4f8fbec932d5`) and drop them in at the same paths to get the real art; no code changes needed.
- **Hero background** (`sx.heroArt` in `app.jsx`) uses `assets/imagery/samurai-alliance.png` — a real moodboard image (small enough to transfer in full), cropped/zoomed (`auto 340% / center 0%`) to keep the silhouetted figures while cropping the font foundry's own specimen text out of frame. **Licensing note:** this file is ENXYCLO Studio's own KARIXBY promotional artwork (the mood board was sourced as tone/texture reference, not licensed photography for reuse) — confirm usage rights before shipping this publicly, or commission/license real hero art instead. `cyber-portrait.png` is unused now; it remains in `assets/imagery/` as an untouched placeholder in case a proper replacement lands later.
