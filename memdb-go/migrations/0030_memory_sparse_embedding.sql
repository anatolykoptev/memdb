-- 0030_memory_sparse_embedding.sql
--
-- Add sparse_embedding column + HNSW index for hybrid retrieval.
--
-- Why: dense embeddings (multilingual-e5-large) handle semantic similarity
-- well but lose on exact-entity lookup. cat1 single-hop questions on the
-- LoCoMo benchmark need exact name matches ("Becoming Nicole", "Bailey",
-- "Charlotte's Web"). Dense alone treats these semantically — "book" near
-- "story" near "novel" — and the specific title gets diluted in top-k.
--
-- SPLADE-v3-distilbert (already loaded in embed-server, served via the
-- /embed_sparse endpoint) emits sparse term-weighted vectors over the BERT
-- vocab (30522 dims). High weights on rare proper nouns + entities. This
-- gives us inverted-index-style exact match alongside dense semantic.
--
-- Hybrid retrieval pattern (Karpathy "use the right tool"): dense for
-- meaning, sparse for exact entities, RRF fusion in the search path.
--
-- pgvector 0.8+ ships sparsevec type + HNSW with sparsevec_ip_ops (inner
-- product, the SPLADE-native distance metric). No new storage system —
-- stays in the existing Postgres.
--
-- Backfill plan: column is NULLable so existing rows keep working. A
-- separate one-shot script generates SPLADE for the existing 657 conv-26
-- rows (and any other production cubes) and UPDATEs in batches.

ALTER TABLE memos_graph."Memory"
    ADD COLUMN IF NOT EXISTS sparse_embedding sparsevec(30522);

-- HNSW index using inner product (SPLADE distance). m=16, ef_construction=64
-- mirror the dense embedding HNSW config that production already runs;
-- conservative defaults that survive memory pressure on the Neoverse-N1
-- ARM host (per the embed-server BATCH_MAX=8 lesson — same hardware
-- ceiling considerations apply here).
--
-- Partial index: only index rows that actually have a sparse embedding.
-- Backfill writes to all rows over time; the partial WHERE keeps the
-- index small until the backfill completes, and avoids storing nullable
-- sparse vectors as zero-weight noise that would hurt query latency.
CREATE INDEX IF NOT EXISTS memory_sparse_embedding_hnsw_idx
    ON memos_graph."Memory"
    USING hnsw (sparse_embedding sparsevec_ip_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE sparse_embedding IS NOT NULL;

-- Operator-friendly comment so anyone querying schema sees the design intent.
COMMENT ON COLUMN memos_graph."Memory".sparse_embedding IS
    'SPLADE-v3-distilbert sparse vector (30522 dims, BERT vocab). NULL for legacy rows pre-backfill. Used in hybrid retrieval RRF fusion alongside dense embedding. Distance metric: inner product (SPLADE-native).';
