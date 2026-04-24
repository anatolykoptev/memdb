# LoCoMo Evaluation Harness (memdb-go)

Reproducible retrieval-quality measurement for **memdb-go** against the
LoCoMo long-conversation benchmark. This is the measurement gate for
Phase D (D1–D8, D10) — every Phase D improvement must be **falsifiable**
against the baseline captured here.

This harness is **separate** from the legacy Python eval at
`evaluation/scripts/locomo/` (that one targets `memdb-api` / Python
service). This directory targets memdb-go's REST API
(`/product/add`, `/product/search`, `/product/chat/complete`).

## What this measures

For every LoCoMo question, we:

1. Ingest the conversation into memdb-go via `/product/add` (one session per
   `/product/add` call, chronological order, deterministic IDs).
2. Query via `/product/search` (retrieval-only) and optionally
   `/product/chat/complete` (end-to-end answer).
3. Score the retrieved/generated answer against the gold reference.

Metrics reported:

| Metric     | Definition |
|------------|-----------|
| `em`       | Exact-match (lowercased, stripped, punctuation-normalized). |
| `f1`       | Token-level F1 between prediction and gold (SQuAD-style). |
| `semsim`   | Cosine similarity of embeddings (uses memdb-go's `LLM_URL` embed-server). Falls back to bag-of-words cosine if `LLM_URL` unset. |
| `hit@k`    | Whether any retrieved memory contains *any* gold evidence token (loose recall signal). |

## Input data

Two LoCoMo files live in `evaluation/data/locomo/`:

- `locomo10.json` — full dataset (10 conversations, ~1990 QA pairs total, list-of-dicts layout).
- `locomo10_rag.json` — same data, dict-of-dicts keyed by conv index.

Both are already committed (~4.7 MB total). No external download required.

**Layout** (`locomo10.json`, list):
```json
[
  {
    "sample_id": "conv-0",
    "conversation": {
      "session_1": [{"speaker": "Melanie", "text": "...", "dia_id": "D1:1"}, ...],
      "session_1_date_time": "1:56 pm on 8 May, 2023",
      "session_2": [...]
    },
    "qa": [
      {"question": "...", "answer": "...", "evidence": ["D1:3"], "category": 2}
    ]
  }
]
```

### Minimal-viable sample

For fast iteration / CI, `sample_conversations.json` + `sample_gold.json`
contain a 1-conversation / 10-QA subset derived from `locomo10.json`
(conv-26, category-1 single-hop questions, deterministic by sort order).
Running `make eval-locomo` without args uses this sample — finishes in <60s
once memdb-go is up.

### 5-category sample (M2 expanded harness)

Setting `LOCOMO_CATEGORIES=1,2,3,4,5` (or passing `--categories=1,2,3,4,5`
to `ingest.py` and `query.py`) enables the expanded 50-QA sample: **10 QAs
per category from conv-26**.

| Category | Type | Phase D features exercised |
|----------|------|---------------------------|
| 1 | Single-hop recall | D1 (basic retrieval fix), D3 (entity linking) |
| 2 | Multi-hop reasoning | D2 (multi-hop reranker), D5 (cross-session links) |
| 3 | Temporal reasoning | D6 (temporal resolution), D8 (session timestamps) |
| 4 | Open-domain / summarisation | D7 (CoT decomposition), D4 (consolidation) |
| 5 | Adversarial ("I don't know") | D10 (hallucination suppression) |

Category 5 gold answers come from `adversarial_answer` in the dataset
(the correct refusal answer).

**Backward compatibility**: the default (`--categories=1` or no env var)
still uses the committed `sample_gold.json` (10 category-1 QAs), so
existing baseline comparisons remain valid. The 5-category mode is opt-in.

Full run (all 10 convs) → `LOCOMO_FULL=1 make eval-locomo`. Expect
5–30 min depending on add/search latency.

## How to run

### Prerequisites

1. **memdb-go running** at `http://localhost:8080` (or set `MEMDB_URL`).
   Easiest: `cd ~/deploy/krolik-server && docker compose up -d memdb-go`.
2. **Auth.** memdb-go requires `Authorization: Bearer` or `X-Service-Secret`.
   Set either:
   - `MEMDB_API_KEY=<plain-master-key>` — Bearer token (matches
     `MASTER_KEY_HASH` in the service env), or
   - `MEMDB_SERVICE_SECRET=$INTERNAL_SERVICE_SECRET` — internal
     service-to-service shortcut (read from `docker exec memdb-go env`).
3. Python 3.10+ with `requests`. No other deps for basic scoring.
   Optional: set `LLM_URL` + `LLM_API_KEY` for semantic similarity
   (uses the same CLIProxyAPI config as memdb-go, `:8317`).

### Quick run (sample)

```bash
cd /home/krolik/src/MemDB
make eval-locomo
```

This writes `evaluation/locomo/results/<commit-sha>.json` with per-QA
scores and aggregate stats.

### 5-category sample run

```bash
LOCOMO_CATEGORIES=1,2,3,4,5 make eval-locomo
```

This runs 50 QAs from conv-26 (10 per category 1-5) and produces a
results JSON with both `aggregate` and `by_category` top-level keys.
Enables per-category measurement of Phase D features (see table above).

### Full LoCoMo run

```bash
LOCOMO_FULL=1 MEMDB_URL=http://localhost:8080 make eval-locomo
```

### Manual steps (for debugging)

```bash
# 1. Ingest (--categories flag is accepted but doesn't affect what's ingested)
python3 evaluation/locomo/ingest.py --sample --memdb-url http://localhost:8080

# 2. Query — default (10 category-1 QAs)
python3 evaluation/locomo/query.py --sample \
    --memdb-url http://localhost:8080 \
    --out evaluation/locomo/results/predictions.json

# 2. Query — 5-category expanded (50 QAs)
python3 evaluation/locomo/query.py --sample \
    --memdb-url http://localhost:8080 \
    --categories=1,2,3,4,5 \
    --out evaluation/locomo/results/predictions-5cat.json

# 3. Score
python3 evaluation/locomo/score.py \
    --predictions evaluation/locomo/results/predictions.json \
    --out evaluation/locomo/results/run.json
```

### Ephemeral stack (CI-like)

`evaluation/locomo/run.sh` orchestrates the full flow. It assumes
memdb-go + postgres + embed-server + redis are **already up** (docker
compose stack in `~/deploy/krolik-server`). Spinning up a fully
ephemeral stack with all four services is deferred — see TODO below.

## How to compare baselines

Every run writes `results/<sha>.json`. To compare two runs:

```bash
python3 evaluation/locomo/compare.py \
    evaluation/locomo/results/baseline-v1.1.0.json \
    evaluation/locomo/results/<new-sha>.json
```

Output is a markdown table with EM / F1 / semsim deltas per category.

## Determinism

- Conversations are ingested in deterministic order (sorted by
  `sample_id`, then session key, then message index).
- All QAs are sorted by `(sample_id, question_idx)` before scoring.
- `chat/complete` calls pass `temperature=0` via memdb-go config
  (memdb-go controls LLM, so the caller can't directly set it —
  the eval assumes MemDB's default LLM is temperature=0 or close).
- Embedder uses `encoding_format=float` for semsim — deterministic for a
  fixed model. Model identity is recorded in the results JSON.

## Baseline

`results/baseline-v1.1.0.json` — captured against current `main` at
commit `cdc5573e` (the Phase D starting point).

**Current baseline is all-zero** (`em=f1=semsim=hit@k=0.000` over 10
category-1 QAs drawn from `conv-26` sessions 1-3). Root cause: the
AGE graph-storage path on current main is broken — `/product/add`
returns HTTP 200 but the `ensure cube failed: column cube_id does not
exist` and `native add failed: function ag_catalog.agtype_in(text)
does not exist` errors from memdb-go logs prevent any long-term
memory from actually being persisted. Explicit preferences
(single-turn smoketest) do survive; the multi-turn LoCoMo flow does
not. See `meta.known_findings` in `baseline-v1.1.0.json`.

This is the **correct** baseline — Phase D exists precisely to fix
these failures, and every improvement (D1-D8, D10) will produce a
concrete positive delta against this zero.

To refresh the baseline after Phase D fixes:

```bash
# start full memdb-go stack first
make eval-locomo                       # sample baseline (~5 min)
LOCOMO_FULL=1 make eval-locomo         # full baseline (hours)
```

Then commit the resulting `results/<sha>.json` as
`results/baseline-v<next-version>.json` (do NOT overwrite
baseline-v1.1.0.json — it's the historical floor).

## TODO

- [ ] **Fully ephemeral stack.** `run.sh` currently assumes the
  memdb-go stack is already running. A true ephemeral run needs
  postgres (`scripts/test-migrations-fresh-db.sh` handles this) **plus**
  embed-server, redis, memdb-go binary build. Track in Phase D.
- [ ] **Baseline capture.** Requires running memdb-go against the
  sample/full LoCoMo. Should be done once the full stack is
  runnable locally without manual setup.
- [ ] **LLM-judge scoring.** The legacy Python harness uses an
  LLM grader (CORRECT / WRONG) on top of EM/F1. That's stronger than
  token overlap but requires budget per run. Add as an opt-in
  (`--llm-judge`) once baseline delta methodology is locked in.

## File layout

```
evaluation/locomo/
├── README.md                     — this file
├── ingest.py                     — push LoCoMo conversations to /product/add
├── query.py                      — query /product/search + /product/chat/complete
├── score.py                      — EM / F1 / semsim scoring
├── compare.py                    — diff two results JSONs
├── run.sh                        — end-to-end orchestrator
├── sample_conversations.json     — 1-conv / 3-session / 10-QA deterministic subset of conv-26
├── sample_gold.json              — gold answers for sample
└── results/
    ├── baseline-v1.1.0.json      — historical floor (current-main reference)
    ├── predictions-*.json        — raw /product/search responses per run (gitignored)
    └── <commit-sha>.json         — per-run scored outputs
```

Intermediate `predictions-*.json` and per-commit score files are not
committed by default (see `evaluation/locomo/.gitignore`). Only
promote a result to a baseline file by copying it to
`results/baseline-v<N>.json` and committing explicitly.
