# Reward Loop — Closed-Loop Learning from User Feedback

## Why

MemDB improves recall by writing memories via the `add` pipeline and querying them via the `search` pipeline. Neither pipeline currently learns from user corrections — when the system returns a wrong answer and the user says "actually, my name is Alex", that signal is discarded.

The reward loop closes this gap in three stages: **Capture (M10) → Curate (M11) → Inject (M12)**.

Inspiration: MemOS `core/reward/` module, which logs every correction into a typed store and uses it to steer the extract prompt at inference time.

---

## Stage M10 — Capture (this PR)

**Goal**: persist every feedback request into a relational store so M11 has data to work with. No background processing.

### Tables

```
memos_graph.feedback_events
  id           BIGSERIAL PK
  user_id      TEXT
  cube_id      TEXT           (nullable — some callers omit it)
  query        TEXT           — the feedback_content string
  prediction   TEXT           — system's answer at time of feedback
                               (empty in M10 stub stage; filled when processFeedback is complete)
  correction   TEXT           (nullable — set when label == 'correction')
  label        TEXT           CHECK (positive|negative|neutral|correction)
  created_at   TIMESTAMPTZ

memos_graph.extract_examples
  id              BIGSERIAL PK
  prompt_kind     TEXT        — 'profile_extract' | 'fine_extract' | ...
  input_text      TEXT        — the conversation snippet
  gold_output     JSONB       — the expected extraction output
  source_event_id BIGINT FK → feedback_events.id   (nullable)
  active          BOOLEAN     DEFAULT true
  created_at      TIMESTAMPTZ
```

### Write path

`handlers.NativeFeedback` calls `persistFeedbackEvent` after `processFeedback` returns. The persistence is fire-and-forget (detached goroutine, 5 s timeout). Errors increment `memdb.feedback.events_total{label="error"}` and are logged at WARN; they never block the response.

### Metric

`memdb.feedback.events_total{label="positive"|"negative"|"neutral"|"correction"|"error"}`

Pre-registered at 0 at startup so Prometheus scrapes the series before the first write.

---

## Stage M11 — Curate

**Goal**: select high-quality corrections from `feedback_events` and promote them to `extract_examples`.

Planned triggers:
- Batch job runs nightly (or on demand via MCP tool).
- Eligibility: `label IN ('correction', 'negative')` AND `correction IS NOT NULL`.
- Threshold: minimum N = 20 eligible events per `prompt_kind` before curation runs
  (prevents noisy early batches from polluting the example set).
- Human review: optional approval flag `extract_examples.active` lets an operator
  veto examples via SQL before they are consumed by M12.

LLM judge step (planned): re-evaluate the `(query, prediction, correction)` triple
to confirm the correction is genuinely better — filters accidental/noisy negatives.

---

## Stage M12 — Inject

**Goal**: use curated examples as few-shot input for the extract prompts.

Planned approach:
- At extract-prompt construction time, query `extract_examples WHERE active AND prompt_kind = $1`
  ordered by recency, limit 5.
- Append as few-shot `user`/`assistant` turns before the current conversation.
- A/B harness: inject only when `REWARD_FEWSHOT_ENABLED=true` (env flag). Metrics:
  `memdb.feedback.events_total{label="positive"}` rate is the primary signal.
- Gradual roll-out: inject for 10% of requests initially, ramp if F1 improves.

---

## Trade-offs

### Privacy

`feedback_events.query` stores the raw `feedback_content` string from the request.
This may contain PII (names, addresses, etc.). Operators must ensure:
- The MemDB Postgres instance is private (not publicly reachable).
- Retention policy: rows older than N days should be purged.
  Suggested: `DELETE FROM memos_graph.feedback_events WHERE created_at < NOW() - INTERVAL '90 days'`
  run as a nightly maintenance job.

### Opt-out

Set `REWARD_CAPTURE_DISABLED=true` to disable the `persistFeedbackEvent` goroutine.
The handler will still process feedback normally; only the DB write is skipped.

Planned implementation (M11): check `os.Getenv("REWARD_CAPTURE_DISABLED")` in
`persistFeedbackEvent` before spawning the goroutine.

### Retention policy

No automatic purge is shipped in M10. M11 will add a configurable TTL env var
`REWARD_RETENTION_DAYS` (default 90) consumed by the nightly batch job.
