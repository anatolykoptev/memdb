-- 0029_wiki_pages.sql — LLM Wiki layer (Karpathy April-2025 pattern).
--
-- Per-cube, LLM-maintained markdown synthesis layer. Compounds over
-- time instead of being re-derived on every retrieve. Mirrored to
-- $UPLOADS_ROOT/memdb-go/wiki/<cube_id>/<slug>.md by the wiki package
-- (internal/wiki) on every write — DB row is authoritative, FS export
-- is best-effort.
--
-- Schema decisions:
--   - cube_id is the partition / tenancy key (security audit C1, same
--     as user_profiles migration 0017). Every row carries it; the
--     unique constraint scopes by cube so different cubes can host the
--     same slug independently.
--   - parent_sources is a UUID array of Memory.id rows that the page
--     synthesizes. Lets the wiki maintainer trace each page back to
--     its raw evidence; lint passes use this for orphan / coverage
--     detection.
--   - embedding is the page-level vector for retrieve-time injection
--     (## Wiki Synthesis block in the chat prompt, ahead of the raw
--     memories block). Same 1024-dim multilingual-e5-large used
--     elsewhere; HNSW index added for cosine cutoff queries.
--   - version increments on every body update. Lets future audit /
--     rollback work without a separate history table.
--   - title + body are plain text; rendering is markdown by
--     convention but the column type makes no assumption.
--
-- Soft-delete is intentionally NOT included: the wiki is meant to be
-- compounding, not erasable. If a page becomes wrong, the LLM rewrites
-- it (version++) — old content is captured in the FS log + audit
-- trail. Hard delete is permitted for cube tear-down only.

CREATE TABLE IF NOT EXISTS memos_graph.wiki_pages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cube_id         TEXT NOT NULL,
    slug            TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    body            TEXT NOT NULL DEFAULT '',
    parent_sources  UUID[] NOT NULL DEFAULT ARRAY[]::UUID[],
    embedding       vector(1024),
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One active page per (cube, slug). Upsert-on-write semantics: the
-- wiki maintainer either creates a new page or updates the existing
-- row (version + body), never two rows competing for the same slug.
CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_pages_cube_slug
    ON memos_graph.wiki_pages (cube_id, slug);

-- Cube-scoped catalog reads: index endpoint listing every page for
-- a tenant.
CREATE INDEX IF NOT EXISTS idx_wiki_pages_cube_updated
    ON memos_graph.wiki_pages (cube_id, updated_at DESC);

-- Vector retrieve at chat time. HNSW with cosine ops matches the
-- existing Memory.embedding strategy (migration 0004).
CREATE INDEX IF NOT EXISTS idx_wiki_pages_embedding
    ON memos_graph.wiki_pages USING hnsw ((embedding::halfvec(1024)) halfvec_cosine_ops);

-- GIN index on parent_sources so lint / audit queries can find pages
-- that reference a specific Memory row. Powers "pages affected by
-- this fact" lookups when a row is merged or invalidated.
CREATE INDEX IF NOT EXISTS idx_wiki_pages_parent_sources
    ON memos_graph.wiki_pages USING gin (parent_sources);

-- Trigger to bump updated_at on every UPDATE so callers can
-- monitor changes without server-side clock juggling.
CREATE OR REPLACE FUNCTION memos_graph.wiki_pages_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_wiki_pages_set_updated_at
    ON memos_graph.wiki_pages;
CREATE TRIGGER trg_wiki_pages_set_updated_at
    BEFORE UPDATE ON memos_graph.wiki_pages
    FOR EACH ROW EXECUTE FUNCTION memos_graph.wiki_pages_set_updated_at();
