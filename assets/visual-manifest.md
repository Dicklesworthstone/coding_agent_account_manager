# Visual Manifest — caam (justakeyboardbetweenus fork)

Size: M — CLI with a vault, a provider layer, and a runner; real architecture,
one process at runtime.

Style bible: One set: a light-grey LEGO terminal-workshop floor on a white
seamless backdrop. One camera: gentle three-quarter angle, slightly elevated.
Accent: glowing translucent teal marks the credential vault and everything it
injects — every other build stays grey/white. Studio softbox lighting
throughout.

Layer definitions (house direction, July 2026): architecture and process
views are FULL LEGO renders (§System diorama pattern) — text-free; every
label lives in a legend table beside the image in the atlas page and README.
A hand-authored modern SVG (direct SVG/D3, never Mermaid) is added only when
precise semantics (branching, exact env var names) genuinely exceed what a
lego render + legend can carry.

| # | Question | Layer | Source | Status |
|---|----------|-------|--------|--------|
| 1 | What is this repo? | identity | prompts/hero.txt | embedded |
| 2 | How is the system shaped? | identity | prompts/architecture-diorama.txt | embedded |
| 3 | How does a run move (staging)? | identity | prompts/run-process-diorama.txt | embedded |
| 4 | How exactly does `caam run claude` resolve a profile into a child process? | diagram | diagrams/token-run-flow.svg (hand-authored SVG) | embedded |
| 5 | Social preview | identity | (hero re-crop 1280×640 → caam-social.png) | embedded |
