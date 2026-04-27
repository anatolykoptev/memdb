# devto-publish.sh — dev.to Publishing CLI

A single-file Bash CLI for publishing Markdown articles to [dev.to](https://dev.to)
via the [Forem API](https://developers.forem.com/api).

Installed at: `~/bin/devto-publish.sh`

---

## Quick Install

The script is already at `~/bin/devto-publish.sh` on the server. All you need is an API key.

**Step 1 — Get your API key**

1. Go to **https://dev.to/settings/extensions**
2. Scroll to "DEV Community API Keys"
3. Create a new key (e.g. "cli-publish")

**Step 2 — Save the key**

Option A — config file (recommended, survives shell restarts):
```bash
mkdir -p ~/.config/devto
echo "your_key_here" > ~/.config/devto/api-key
chmod 600 ~/.config/devto/api-key
```

Option B — environment variable (session only):
```bash
export DEVTO_API_KEY=your_key_here
```

**Step 3 — Test**
```bash
~/bin/devto-publish.sh list
```

---

## Subcommands

### `draft` — Create a new draft article

```bash
~/bin/devto-publish.sh draft <markdown_file> \
    --title "Your Article Title" \
    --tags ai,opensource,go \
    --cover https://example.com/og.png \
    --canonical https://yourblog.com/article/
```

- Creates article with `published: false`
- Prints full JSON response + draft URL
- CLI args override frontmatter values
- Returns: article ID for subsequent commands

### `list` — List all articles

```bash
~/bin/devto-publish.sh list
```

Shows up to 30 most recent articles (drafts + published) with ID, title, published status, URL.

### `view` — Inspect a single article

```bash
~/bin/devto-publish.sh view 1234567
```

Shows metadata + first 200 characters of body. Useful to confirm draft looks right before publishing.

### `update` — Replace body and metadata

```bash
~/bin/devto-publish.sh update 1234567 article.md \
    --title "Updated Title" \
    --tags ai,go
```

Preserves the current `published` state (draft stays draft, published stays published).

### `publish` — Flip draft to published

```bash
~/bin/devto-publish.sh publish 1234567
```

Sets `published: true`. Article goes live immediately.

### `unpublish` — Flip published back to draft

```bash
~/bin/devto-publish.sh unpublish 1234567
```

Sets `published: false`. Article becomes a draft again.

---

## Markdown Frontmatter

The script reads YAML frontmatter from the top of the Markdown file. CLI flags override frontmatter values.

```markdown
---
title: "How we hit 72.5% on LoCoMo with 6 weeks of compound improvements"
tags: ai,opensource,go,rust
cover: https://memdb.ai/og.png
canonical_url: https://memdb.ai/blog/launch/
---

Your article body starts here...
```

Supported frontmatter keys:
- `title` — article title
- `tags` — comma-separated list (max 4)
- `cover` — cover image URL
- `canonical_url` — canonical URL for cross-posts

---

## Common Workflow

```bash
# 1. Write your article
vim ~/articles/my-post.md

# 2. Create draft and review in browser
~/bin/devto-publish.sh draft ~/articles/my-post.md \
    --title "My Post" \
    --tags ai,opensource

# → Opens in browser: https://dev.to/dashboard
# → Note the article ID from the output (e.g. 1234567)

# 3. (Optional) Update after edits
~/bin/devto-publish.sh update 1234567 ~/articles/my-post.md

# 4. Review draft at https://dev.to/dashboard

# 5. Publish when ready
~/bin/devto-publish.sh publish 1234567

# 6. Verify
~/bin/devto-publish.sh list
```

---

## Tags Conventions for Tech Articles

dev.to recommends 3-4 relevant tags. Popular combinations for technical content:

| Topic | Suggested tags |
|-------|---------------|
| Open source Go/Rust tool | `opensource`, `go`, `rust`, `programming` |
| AI / LLM tooling | `ai`, `llm`, `machinelearning`, `opensource` |
| Self-hosted infra | `selfhosted`, `devops`, `opensource`, `go` |
| Agent memory / RAG | `ai`, `rag`, `machinelearning`, `opensource` |
| Launch post | `showdev`, `opensource`, `ai` |

**Important:** dev.to strictly limits to **4 tags maximum**. The script will error if more are passed.

---

## Canonical URL Strategy

Setting `canonical_url` tells dev.to (and search engines) that dev.to is a cross-post,
not the original. This prevents duplicate-content SEO penalties.

**When you have a blog** (e.g. `memdb.ai/blog/`):
```
canonical_url: https://memdb.ai/blog/launch/
```

**When the article lives on GitHub** (no blog yet):
```
canonical_url: https://github.com/anatolykoptev/memdb#readme
```

**If dev.to is the canonical source** (no cross-post planned):
Omit `canonical_url` entirely — dev.to will use its own URL.

---

## Known Limits

| Limit | Value |
|-------|-------|
| Tags per article | 4 max |
| Body length | 100,000 characters |
| Description | 250 characters |
| API key rotation | Per-key at dev.to/settings/extensions |
| Rate limits | Undocumented; ~1 req/sec is safe |

---

## Dependencies

- `bash` 4.0+
- `curl`
- `jq` (install: `apt-get install jq` or `brew install jq`)

---

## Troubleshooting

**`ERROR: DEVTO_API_KEY not set`**
Follow the setup steps above.

**`curl: (22) The requested URL returned error: 422`**
API validation failed — usually malformed JSON or exceeding tag limit. Check the response body.

**`curl: (22) The requested URL returned error: 401`**
Invalid or expired API key. Regenerate at https://dev.to/settings/extensions.

**Article created but no cover image showing**
dev.to fetches cover images asynchronously — wait a few minutes or force-refresh the draft preview.
