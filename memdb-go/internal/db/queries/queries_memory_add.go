package queries

// queries_memory_add.go — SQL queries for the native add pipeline.
// Covers: insert, dedup helpers, content-hash check, working memory cleanup.
//
// NOTE: the Memory vertex's id column is an AGE-managed graphid (auto-generated).
// The stable UUID identity lives in properties->>(('id'::text)) and is the column
// the Go layer uses for all lookups / updates / deletes. Never bind a UUID string
// into the id column — that fails with SQLSTATE 22P02 (invalid input for graphid).

// DeleteMemoryByPropID deletes a memory node matching properties->>(('id'::text)).
// Used as the first half of an upsert (DELETE then INSERT).
// Args: $1 = id (text, UUID)
const DeleteMemoryByPropID = `
DELETE FROM %[1]s."Memory" WHERE properties->>(('id'::text)) = $1`

// InsertMemoryNode inserts a new memory node with properties and embedding.
// The id column is AGE-managed (graphid) and auto-generated — we never bind it.
// properties->>(('id'::text)) carries the stable UUID identity used by all other queries.
// Used as the second half of an upsert (DELETE then INSERT).
// Args: $1 = properties (jsonb), $2 = embedding (text, cast to vector(1024))
const InsertMemoryNode = `
INSERT INTO %[1]s."Memory"(properties, embedding)
VALUES ($1::text::agtype, $2::vector(1024))`

// UpdateMemoryNodeFull updates the memory text, embedding, and updated_at for an existing activated node.
// Used by the fine-mode dedup-merge pipeline when JudgeDedupMerge returns action="update".
// Args: $1 = memory_id (properties->>(('id'::text))), $2 = new memory text, $3 = new embedding (text cast to vector(1024)), $4 = updated_at (text)
const UpdateMemoryNodeFull = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'memory',     $2::text,
            'updated_at', $4::text
        ))::text
    )::agtype,
    embedding = CASE WHEN $3::text = '' THEN embedding ELSE $3::vector(1024) END
WHERE properties->>(('id'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'`

// CheckContentHashExists checks whether an activated memory with the given content_hash exists for a user.
// Args: $1 = content_hash (text), $2 = user_name (text)
const CheckContentHashExists = `
SELECT EXISTS(
    SELECT 1 FROM %[1]s."Memory"
    WHERE properties->('info'::text)->>(('content_hash'::text)) = $1
      AND properties->>(('user_name'::text)) = $2
      AND properties->>(('status'::text)) = 'activated'
)`

// CleanupOldestWorkingMemory deletes the oldest WorkingMemory nodes beyond a keep limit.
// Keeps the N newest (by updated_at DESC) and deletes the rest.
// The inner SELECT / outer DELETE both match on the graphid id column — this is
// self-consistent AGE-native plumbing and stays as-is.
// Args: $1 = user_name (text), $2 = keep_count (int)
const CleanupOldestWorkingMemory = `
DELETE FROM %[1]s."Memory"
WHERE id IN (
    SELECT id FROM %[1]s."Memory"
    WHERE properties->>(('user_name'::text)) = $1
      AND properties->>(('memory_type'::text)) = 'WorkingMemory'
      AND properties->>(('status'::text)) = 'activated'
    ORDER BY (properties->>(('updated_at'::text))) DESC NULLS LAST
    OFFSET $2
)`

// CleanupOldestWorkingMemoryReturning is like CleanupOldestWorkingMemory but
// returns the property UUID of each deleted node so callers can evict them from VSET.
// Args: $1 = user_name (text), $2 = keep_count (int)
const CleanupOldestWorkingMemoryReturning = `
DELETE FROM %[1]s."Memory"
WHERE id IN (
    SELECT id FROM %[1]s."Memory"
    WHERE properties->>(('user_name'::text)) = $1
      AND properties->>(('memory_type'::text)) = 'WorkingMemory'
      AND properties->>(('status'::text)) = 'activated'
    ORDER BY (properties->>(('updated_at'::text))) DESC NULLS LAST
    OFFSET $2
)
RETURNING properties->>(('id'::text)) AS node_id`
