# Scheduler Skills Hot-Reload

Operator guide for `MEMDB_SCHEDULER_SKILLS_DIR` — introduced in Phase 5 (PR #263).

## Overview

By default the 8 scheduler system prompts are baked into the binary. Updating them requires a rebuild and dozor redeploy (~3–5 min). Setting `MEMDB_SCHEDULER_SKILLS_DIR` activates a workspace tier that the catalog checks first; edits to files on disk are picked up on the next request via mtime cache — **no container restart required**.

This is symmetric to the existing `MEMDB_D10_SKILL_PATH` and `MEMDB_ATOMIC_SKILL_PATH` env vars.

## Activation

1. Add to `~/deploy/server-config/.env`:

```
MEMDB_SCHEDULER_SKILLS_DIR=/host-skills/scheduler
```

The compose volume `~/deploy/server-config/skills:/host-skills:ro` is already present; no compose change needed.

2. Create a skill override directory:

```bash
mkdir -p ~/deploy/server-config/skills/scheduler/memory-consolidator
```

3. Drop a `SKILL.md` with YAML frontmatter at that path:

```bash
cat > ~/deploy/server-config/skills/scheduler/memory-consolidator/SKILL.md <<'EOF'
---
name: memory-consolidator
description: Custom override for memory-consolidator scheduler prompt
---
Your updated prompt body here...
EOF
```

4. The next scheduler request will pick up the new file — verify in metrics:

```bash
curl -s http://127.0.0.1:9000/metrics \
  | grep memdb_skillkit_body_calls_total \
  | grep scheduler
```

Look for `source="scheduler-workspace"` counter incrementing alongside (or replacing) `source="scheduler-builtin"`.

## Skill names

The 8 baked-in names (also the expected directory names under the workspace dir):

| Directory name | Role |
|---|---|
| `memory-consolidator` | Merges and deduplicates raw memories |
| `feedback-memory-curator` | Curates feedback-derived memories |
| `preference-extractor` | Extracts user preferences |
| `session-compactor` | Compacts session context |
| `episodic-tier-archivist` | Archives episodic tier entries |
| `semantic-tier-abstractor` | Abstracts semantic tier content |
| `relation-detector` | Detects entity relations |
| `wm-enhancement-extractor` | Enhances working memory extractions |

## Fallback behavior

The workspace tier is checked first; if the skill directory or `SKILL.md` is absent the request falls through to the embedded builtin. You can override a single skill while leaving the other 7 on their embedded versions.

## Disabling

Unset or remove `MEMDB_SCHEDULER_SKILLS_DIR` from `.env` and restart the container. The catalog reverts to single-tier embedded mode.

## Frontmatter note

The directory name — not the frontmatter `name:` field — is the lookup key in `DirTier`. A mismatch between directory name and frontmatter `name:` is cosmetic only; the `name:` field is stored in `SkillInfo.Metadata` but is not used for routing. Always name the directory after the canonical skill name listed above.
