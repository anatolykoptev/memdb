-- backfill_event_dates_regex.sql — non-LLM enrichment for F11 event_dates.
--
-- Scans every Memory row that lacks an event_dates property and runs three
-- regex passes against properties.memory:
--   1) ISO YYYY-MM-DD anywhere in the text
--   2) "Month DD, YYYY" / "Month DDth YYYY" — named month + day + year
--   3) "DD Month YYYY" / "DDth Month YYYY" — day + named month + year
--
-- Hits are de-duplicated and written back into properties.event_dates as a
-- jsonb array of ISO YYYY-MM-DD strings. The same field is consumed by F11
-- search-time temporal lookups (db/postgres_temporal.go::SearchMemoriesByDateRange)
-- and the F7 partial index `idx_event_dates`.
--
-- Why this script (not a migration):
-- - mode=raw is contractually a NO-LLM zone (CLAUDE.md "Add-mode contract").
--   This regex pass stays inside that contract — zero LLM calls, deterministic
--   output. It is the cheap-proxy alternative to switching the call site to
--   mode=fine, which is the supported path for full graph extraction.
-- - It is opt-in ops tooling, not part of the schema lifecycle. Migration files
--   must be idempotent + always-safe to re-run on every container start;
--   this is a one-shot enrichment that the operator decides to run.
-- - The pg_temp function disappears at session end, so re-running is naturally
--   idempotent: the WHERE clause already skips rows that already have
--   event_dates populated.
--
-- Usage:
--   docker exec -i postgres psql -U memos -d memos < backfill_event_dates_regex.sql
-- Or via the wrapper:
--   bash evaluation/locomo/scripts/backfill_event_dates_regex.sh
--
-- Cost: regex-only, no network, no LLM. On a 16k-row corpus the function
-- definition + UPDATE typically completes in 10-30 s. The pre-filter
-- `properties.memory ~* '\m(20\d\d|january|...|december)\M'` keeps the
-- regex_matches passes off rows that obviously cannot match.

CREATE OR REPLACE FUNCTION pg_temp.extract_event_dates(t text)
RETURNS jsonb
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
  iso_dates text[];
  month_named text[][];
  month_first text[][];
  m text;
  d int;
  y int;
  month_num int;
  result text[] := ARRAY[]::text[];
  month_map jsonb := '{
    "january":1,"february":2,"march":3,"april":4,"may":5,"june":6,
    "july":7,"august":8,"september":9,"october":10,"november":11,"december":12,
    "jan":1,"feb":2,"mar":3,"apr":4,"jun":6,"jul":7,"aug":8,"sep":9,"oct":10,"nov":11,"dec":12
  }'::jsonb;
BEGIN
  FOR m IN SELECT (regexp_matches(t, '\m(20[0-2][0-9]-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01]))\M', 'g'))[1]
  LOOP
    IF NOT (m = ANY(result)) THEN result := array_append(result, m); END IF;
  END LOOP;

  FOR month_named IN SELECT regexp_matches(
    t,
    '\m(January|February|March|April|May|June|July|August|September|October|November|December)\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(20[0-2][0-9])\M',
    'gi'
  )
  LOOP
    month_num := (month_map->>lower(month_named[1]))::int;
    d := month_named[2]::int;
    y := month_named[3]::int;
    IF month_num IS NOT NULL AND d BETWEEN 1 AND 31 THEN
      m := to_char(make_date(y, month_num, d), 'YYYY-MM-DD');
      IF NOT (m = ANY(result)) THEN result := array_append(result, m); END IF;
    END IF;
  END LOOP;

  FOR month_first IN SELECT regexp_matches(
    t,
    '\m(\d{1,2})(?:st|nd|rd|th)?\s+(January|February|March|April|May|June|July|August|September|October|November|December),?\s+(20[0-2][0-9])\M',
    'gi'
  )
  LOOP
    month_num := (month_map->>lower(month_first[2]))::int;
    d := month_first[1]::int;
    y := month_first[3]::int;
    IF month_num IS NOT NULL AND d BETWEEN 1 AND 31 THEN
      m := to_char(make_date(y, month_num, d), 'YYYY-MM-DD');
      IF NOT (m = ANY(result)) THEN result := array_append(result, m); END IF;
    END IF;
  END LOOP;

  IF array_length(result, 1) IS NULL THEN
    RETURN NULL;
  END IF;
  RETURN to_jsonb(result);
EXCEPTION WHEN OTHERS THEN
  RETURN NULL;
END;
$$;

-- UPDATE rows where regex finds dates
WITH targets AS (
  SELECT ctid, pg_temp.extract_event_dates(properties::text::jsonb->>'memory') AS dates
  FROM memos_graph."Memory"
  WHERE NOT (properties::text::jsonb ? 'event_dates'
             AND jsonb_array_length(properties::text::jsonb->'event_dates') > 0)
    AND properties::text::jsonb->>'memory' ~* '\m(20[0-2][0-9]|january|february|march|april|may|june|july|august|september|october|november|december)\M'
)
UPDATE memos_graph."Memory" m
SET properties = jsonb_set(
      m.properties::text::jsonb,
      '{event_dates}',
      t.dates,
      true
    )::text::agtype
FROM targets t
WHERE m.ctid = t.ctid AND t.dates IS NOT NULL
RETURNING properties::text::jsonb->'event_dates' AS new_dates;
