# Hero Image Prompt — "Open Marketplace for Bandwidth"

Prompts for generating a hero/banner image with a third-party AI image
generator (Midjourney, DALL·E 3, Ideogram, Stable Diffusion, etc.), built
around ARFL's brand line: **"Privacy in the Dark Cloud."**

Copy the tagline exactly:

> The open marketplace for bandwidth
> Strangers, sharing bandwidth with strangers. No operator sits in the
> middle — Bitcoin is the medium of exchange.

## Recommended approach

Most image generators render long paragraphs of text poorly. Use **Prompt
A** to generate a clean abstract background/hero art with no baked-in
text, then overlay the tagline yourself in a design tool (Figma, Canva,
CSS). Use **Prompt B** only with models known to render text accurately
(DALL·E 3, Ideogram, Recraft).

---

## Prompt A — Abstract background, no text (recommended)

```
A dark, cinematic digital illustration representing a decentralized
peer-to-peer bandwidth marketplace. Two anonymous silhouetted figures,
rendered as low-poly wireframe humanoid outlines in deep navy and black,
face each other across a void, connected by a single glowing thread of
flowing light-orange data particles — representing bandwidth being
shared directly between strangers with no intermediary. Around them,
a faint decentralized mesh network of small nodes and thin glowing
lines fades into darkness, evoking a "dark cloud." A subtle, minimal
Bitcoin symbol (₿) glows softly at the midpoint of the connecting
thread, like a spark of exchange. No central hub, no tower, no visible
authority figure — emphasize direct, peer-to-peer connection.
Color palette: near-black background (#0a0e1a), deep navy, electric
cyan accents, warm Bitcoin-orange (#f7931a) glow. Style: minimalist
tech-noir, high contrast, soft volumetric glow, subtle film grain,
ultra-clean composition with generous negative space for text overlay
in the upper-left third. No text, no logos, no watermarks.
--ar 16:9 --style raw --v 6
```

**Negative prompt** (for tools that support it):
```
text, words, letters, logo, watermark, signature, people's faces,
photorealistic humans, clutter, busy background, bright daylight,
cartoon, corporate stock photo, centralized server tower, padlock icon
```

**Square/social variant:** change `--ar 16:9` to `--ar 1:1`.
**Story/vertical variant:** change to `--ar 9:16`.

---

## Prompt B — With tagline baked in (text-capable models only)

```
Minimalist tech-noir poster design, near-black background with a
faint decentralized mesh network glowing in deep navy and cyan. Two
small anonymous silhouetted figures on opposite sides connected by a
single thread of warm Bitcoin-orange light, no central node between
them. Bold clean sans-serif headline text at the top reads
"The open marketplace for bandwidth" in white. Below it, smaller
light-gray text reads "Strangers, sharing bandwidth with strangers.
No operator sits in the middle — Bitcoin is the medium of exchange."
Generous margins, strong typographic hierarchy, modern fintech/crypto
aesthetic, high contrast, subtle glow, 16:9 banner composition.
```

---

## Style keyword bank

Mix and match if you want to try variants:

- **Mood:** tech-noir, cinematic, minimal, futuristic, quiet confidence
- **Palette:** near-black `#0a0e1a`, navy `#101a33`, cyan `#3bd6ff` accent,
  Bitcoin-orange `#f7931a` accent, off-white text
- **Motifs:** mesh network / nodes, WireGuard-style tunnel lines, glowing
  thread between two points, faint cloud silhouette made of circuitry,
  small ₿ glyph as the "spark" of exchange — avoid padlocks, shields,
  or centralized-server imagery (contradicts the "no operator" message)
- **Avoid:** stock-photo hands shaking, corporate blue gradients, literal
  WiFi icons, anything implying a central authority or company logo in
  the middle of the exchange
