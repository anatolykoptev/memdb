-- Migration 0016 — feedback_events + extract_examples tables.
-- Purpose: foundation tables for the M11 reward / RL feedback loop.
--   feedback_events  — per-request capture of user corrections and labels.
--   extract_examples — curated gold examples for few-shot injection (M12).
--
-- Schema: memos_graph (verified as the canonical schema for all MemDB tables).
-- Idempotent: CREATE TABLE IF NOT EXISTS so re-runs are safe.

CREATE TABLE IF NOT EXISTS memos_graph.feedback_events (
    id           BIGSERIAL PRIMARY KEY,
    user_id      TEXT NOT NULL,
    cube_id      TEXT,
    query        TEXT NOT NULL,
    prediction   TEXT NOT NULL,
    correction   TEXT,
    label        TEXT NOT NULL CHECK (label IN ('positive','negative','neutral','correction')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_feedback_events_user_created
    ON memos_graph.feedback_events (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS memos_graph.extract_examples (
    id              BIGSERIAL PRIMARY KEY,
    prompt_kind     TEXT    NOT NULL,  -- e.g. 'profile_extract', 'fine_extract'
    input_text      TEXT    NOT NULL,
    gold_output     JSONB   NOT NULL,
    source_event_id BIGINT  REFERENCES memos_graph.feedback_events(id),
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_extract_examples_prompt_active
    ON memos_graph.extract_examples (prompt_kind) WHERE active;
