# `docs/assets/` — README visual assets

This directory holds visual assets that render inline in the top-level `README.md` and on
the GitHub project page. Keep files small (target < 500 KB each) so the README loads fast.

## Shipped

| File | Used in | Notes |
|---|---|---|
| `architecture.svg` | `README.md` hero | Six-container architecture diagram. Pure SVG, ~7 KB, scales to any width. Edit by hand — no build step. |

## Planned (TODOs — not shipped yet)

These slots have HTML-comment placeholders at their target locations in `README.md`. Land
the asset, then uncomment the `<img>` tag.

### `demo.gif` — terminal screencast (~30 s, ≤ 2 MB)

Shows an end-to-end flow: `docker compose up` → `curl /product/add` → `curl /product/search`
→ memory comes back. Goal: prove the "5-minute quick start" claim above the fold.

Recording recipe:

```bash
# 1. Record
asciinema rec demo.cast --idle-time-limit 1.5 --max-wait 2 --command "bash"
# in the recording shell, run the quick-start commands at a comfortable pace, then `exit`

# 2. Convert to GIF
agg --theme monokai --speed 1.4 --font-size 16 demo.cast docs/assets/demo.gif
# tools: `cargo install --git https://github.com/asciinema/agg`

# 3. Verify size
du -h docs/assets/demo.gif    # target ≤ 2 MB; if larger, drop FPS or trim
```

Then in `README.md`, uncomment the `<img src="docs/assets/demo.gif" ...>` line.

### `telegram-bot-demo.png` — screenshot (~1200×800, ≤ 300 KB)

Side-by-side: Telegram chat on the left ("user: I love hiking" → "user, in a new chat
the next day: suggest a weekend activity" → bot answers using stored preference), MemDB
log lines on the right showing the `/product/search` hit. Use a real bot, blur the
profile picture.

### `benchmark-comparison.svg` — bar chart (~800×400)

LLM-Judge score: MemDB vs Mem0 vs Letta vs Zep vs Memobase. Land **after** M9 Stage 3
re-run finishes (currently OOM-deferred — see `evaluation/locomo/MILESTONES.md`). Numbers
are kept honest with `?` markers in the README comparison table until measurements land.

Build with the same Go script that emits the LoCoMo PNG charts under `evaluation/locomo/`
— add an `--out docs/assets/benchmark-comparison.svg` flag.

### `star-history.svg` — embedded chart

Use [star-history.com](https://star-history.com/) to generate an embed for
`anatolykoptev/memdb`. Two options:

1. Hot-link the live chart (always current, requires external request from GitHub viewers).
2. Snapshot it as SVG and commit (no external request, goes stale).

Pick option 1 unless we hit a privacy/CSP requirement to self-host.

## Conventions

- **Format**: SVG > PNG > GIF > JPG. Use SVG for diagrams, PNG for screenshots, GIF only
  for short animations.
- **Naming**: `kebab-case`, no version suffixes (`architecture.svg`, not `architecture-v2.svg`
  — use git history for revisions).
- **Width**: design for 800–1200 px logical width; `width="100%"` in the README handles
  responsive scaling.
- **Optimization**: run new PNGs through `oxipng -o4`, new SVGs through `svgo` before
  committing.
