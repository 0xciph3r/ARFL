# UI Kit — Private-Connection Client

ARFL's mobile client for the decentralised VPN. An **interactive click-thru prototype**: connect → live tunnel with per-bundle Lightning spend → switch exit nodes → wallet → run-a-node (earn). **Original on-brand interpretation** (no source app was provided).

## Run
Open `index.html` via an HTTP server (e.g. `npx serve web/ui_kits/app`). React 18 + Babel + Lucide from CDN; tokens from `../../colors_and_type.css`.

## Try it
1. **Connect** — tap the power orb. It shows *Establishing tunnel…* (gold), then *Connected* (cyan): the wireframe globe pulses, live Down/Up throughput counts up, and **sats spent** ticks via per-bundle Lightning billing.
2. **Nodes** tab — pick an exit node (Tokyo, Reykjavík, Zürich, Singapore, Montréal). Each shows ping, sats/GB, and live load. Selecting one returns you to Connect.
3. **Wallet** tab — Lightning channel balance in sats, top-up/withdraw, per-session spend history.
4. **Earn** tab — system operator view: start/stop a node, watch sats earned tick up, peers/throughput readouts. Advertised over Nostr, settled via Fedimint (50/50 split between entry and exit node, no protocol fee).

## Files
- `index.html` — shell, radial-lit black stage, keyframes (pulse / spin / scan / rise).
- `components.jsx` — `PhoneFrame` (notch, status bar, cyan rim-glow), `TabBar` (Connect/Nodes/Wallet/Earn), `AppButton`, `Screen` scaffold, `Icon`/`useLucide`.
- `screens.jsx` — `Globe` (wireframe network motif), `ConnectScreen`, `NodesScreen`, `WalletScreen`, `EarnScreen`, `Readout`, `ScreenTitle`. Node data in `NODES`.
- `app.jsx` — connection lifecycle, throughput simulation, per-bundle billing, node switching, operator earnings.

## Patterns demonstrated
- Connection-state machine (disconnected / connecting / connected) driving colour, glow, globe spin + pulse.
- "No logs. No account. Paid per bundle over Lightning." — privacy + pay-per-use messaging throughout.
- Mono for all protocol values (node IDs, ping, sats, throughput); KARIXBY (substitute) display for screen titles; katakana accent on titles.

## Known gaps
- Onboarding / key-setup and a Settings screen are not built (Connect/Nodes/Wallet/Earn are the flows).
- Billing math is a simplified visual, not a real Lightning meter.
- This is our **best guess at the product** — confirm the real client's flows (see root README ASK). This kit has no external image dependencies (all visuals are procedural SVG/CSS), so it renders fully out of the box.
