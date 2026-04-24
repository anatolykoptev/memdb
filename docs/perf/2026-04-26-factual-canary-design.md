# Factual Answer-Style Canary — Design & Runbook

**Stream**: M8 Stream 8 PRODUCT  
**Date**: 2026-04-26  
**Status**: Ready to deploy

## Background

M7 Stream F (latency report `2026-04-25-m7-latency-report.md`) showed:

- `answer_style=factual` is **2.1× faster at p95** than `conversational`.
- Factual F1 on the LoCoMo benchmark is **higher**.

Goal: validate that the factual prompt remains better in production on the `memos` cube
with real user traffic before promoting it as the default.

## Methodology

### A/B Split

10% of users are routed to `answer_style=factual`; the remaining 90% continue with the
current default (`conversational` or whatever `MEMDB_DEFAULT_ANSWER_STYLE` is set to).

Activation: set `MEMDB_FACTUAL_CANARY_PCT=10` in the `memdb-go` environment.

### Bucketing

Bucketing is hash-based and sticky per `user_id`:

```
sha256(user_id)[:4]  →  big-endian uint32  →  mod 100  →  bucket in [0, 100)
if bucket < MEMDB_FACTUAL_CANARY_PCT  →  factual
```

- No state stored anywhere — same user always gets the same bucket.
- Zero flapping between requests or after restarts.
- Empty `user_id` → bucket = 10 (outside [0,10), so not in the default 10% canary).

### Precedence

`resolveAnswerStyle` applies styles in this order (highest wins):

1. **Per-request `answer_style` field** (non-empty) — operator or client explicit choice.
2. **Canary bucket** — if `MEMDB_FACTUAL_CANARY_PCT > 0` and user is in bucket.
3. **`MEMDB_DEFAULT_ANSWER_STYLE`** env — server-wide default.
4. **Empty** — `buildSystemPrompt` falls back to conversational template.

## Metrics to Watch

All metrics are emitted by `memdb-go` via OpenTelemetry.

| Metric | Labels | Purpose |
|--------|--------|---------|
| `memdb.chat.answer_acceptance_total` | `style`, `outcome=served` | Request volume per style (both buckets) |
| `memdb.chat.prompt_template_used_total` | `template` | Cross-check: factual vs conversational split |
| LLM latency histogram | — | p50/p95 by style (query with `style` label if exporter supports it) |
| HTTP error rate | — | 5xx spike would indicate prompt regression |

### Prometheus Queries

```promql
# Canary request share
sum(rate(memdb_chat_answer_acceptance_total{outcome="served"}[5m])) by (style)

# Serving ratio
rate(memdb_chat_answer_acceptance_total{style="factual",outcome="served"}[5m])
  /
rate(memdb_chat_answer_acceptance_total{outcome="served"}[5m])
```

## Activation

```bash
# On memdb-go container or environment:
MEMDB_FACTUAL_CANARY_PCT=10

# Restart is required to pick up the new env var.
# No code change needed.
```

## Decision Criteria (after 24 hours of production traffic)

Evaluate at T+24h using the metrics above.

### GO — promote factual to default

All of the following must hold:

- Factual **p95 latency ≥ 1.5× faster** than conversational (matches M7 finding in prod).
- Factual **error rate within 1 percentage point** of conversational.
- **No downstream complaints** filed by users or operators against the factual response style.

Action: set `MEMDB_DEFAULT_ANSWER_STYLE=factual` and `MEMDB_FACTUAL_CANARY_PCT=0`.

### HOLD — keep canary running

Any condition:

- p95 improvement confirmed but less than 1.5×.
- Error rates within tolerance but traffic volume was too low (<100 factual requests).
- Ambiguous user feedback.

Action: wait 7 more days, re-evaluate.

### ROLLBACK — immediate

Any of the following:

- Factual **error rate > 1 pp above conversational** (even transient spike).
- User complaints about response quality attributed to the canary bucket.
- Any LLM quota exhaustion that disproportionately affects the factual path.

Action: see Rollback Procedure below.

## Rollback Procedure

Rollback is **instant and requires no rebuild**:

```bash
# Set the canary percentage to 0 — no users get factual via canary.
MEMDB_FACTUAL_CANARY_PCT=0

# Reload env (depends on deploy method):
#   Docker Compose:  docker compose up -d --no-deps --force-recreate memdb-go
#   Systemd:         systemctl --user restart memdb-go
```

If `MEMDB_DEFAULT_ANSWER_STYLE=factual` was already set (post-promotion rollback):

```bash
MEMDB_DEFAULT_ANSWER_STYLE=conversational
MEMDB_FACTUAL_CANARY_PCT=0
# Restart service as above.
```

No database migration required — bucket assignments are stateless.

## Out of Scope

- **Deploying the canary**: operator action — set `MEMDB_FACTUAL_CANARY_PCT=10` and restart.
- **24h observation report**: `docs/perf/2026-04-27-factual-canary-results.md` — written by whoever observes the window.
- **Per-cube canary knob**: M9 backlog item.
- **User-feedback signal collection** (`accepted`/`rejected` outcomes on the acceptance counter): separate concern, future PR.
