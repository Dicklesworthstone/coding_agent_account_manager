# Visual Manifest — caam (justakeyboardbetweenus fork)

Size: M — CLI with a vault, a provider layer, and a runner; real architecture,
one process at runtime.

Style bible: Full-color LEGO-city sets — colorful bricks, busy skyline
backdrops, photorealistic LEGO product photography with shallow depth of
field. Brick-built banner typography (red frame, yellow panel, blue studded
letters) plus white station signposts and sticker nameplates carry all
naming in-scene; signage is 1–3 words, never sentences. Providers are
personified as robot characters with playful logo-riffs — evoke each
brand's shape and color, never an exact trademark (warm-orange starburst
CLAUDE, monochrome-hex CODEX, sparkle-gem GEMINI, llama OLLAMA…). The
credential vault stays the glowing translucent-teal centerpiece across
every scene: one focal story beat dominates via light and scale — attention
hierarchy, not color removal.

Layer definitions (house direction, July 2026): architecture and process
views are FULL LEGO renders carrying their own short brick-built signage
(banner, signposts, nameplates). Legends beside the image cover only what
signs don't name — env var names, file modes, profile kinds. A
hand-authored modern SVG (direct SVG/D3, never Mermaid) is added only when
precise semantics (branching, exact env var names) genuinely exceed what a
signposted render + slim legend can carry.

| # | Question | Layer | Source | Status |
|---|----------|-------|--------|--------|
| 1 | What is this repo? | identity | prompts/hero.txt | embedded |
| 2 | How is the system shaped? | identity | prompts/architecture-diorama.txt | embedded |
| 3 | How does a run move (staging)? | identity | prompts/run-process-diorama.txt | embedded |
| 4 | How exactly does `caam run claude` resolve a profile into a child process? | diagram | diagrams/token-run-flow.svg (hand-authored SVG) | embedded |
| 5 | Social preview | identity | (hero re-crop 1280×640 → caam-social.png) | embedded |
