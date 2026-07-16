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
- **Hero imagery:** `../../assets/imagery/cyber-portrait.png`, `../../assets/imagery/network-globe.png`, and `../../assets/mascot/satoshi.png` are placeholder art in this repo, not the real moodboard-derived imagery — the Claude Design MCP caps file reads at 256 KiB and the real assets exceed that. Pull the actual files from the source Claude Design project (`claude.ai/design/p/6bf8b225-2d60-4fae-a177-4f8fbec932d5`) and drop them in at the same paths to get the real hero art; no code changes needed.
