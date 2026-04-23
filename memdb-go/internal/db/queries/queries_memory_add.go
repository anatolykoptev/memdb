package queries

// queries_memory_add.go — SQL queries for the native add pipeline.
// Covers: insert, dedup helpers, content-hash check, working memory cleanup.

// DeleteMemoryByPropID deletes a memory node by its table id (which equals properties->>(('id'::text))).
// Used as the first half of an upsert (DELETE then INSERT).
// Args: $1 = id (text, UUID)
const DeleteMemoryByPropID = `
DELETE FROM %[1]s."Memory" WHERE id = $1`

// InsertMemoryNode inserts a new memory node with id, properties and embedding.
// The table id column must match properties->>(('id'::text)).
// Used as the second half of an upsert (DELETE then INSERT).
// Args: $1 = id (text), $2 = properties (jsonb), $3 = embedding (text, cast to vector(1024))
const InsertMemoryNode = `
INSERT INTO %[1]s."Memory"(id, properties, embedding)
VALUES ($1, ag_catalog.agtype_in($2::text), $3::vector(1024))`

// UpdateMemoryNodeFull updates the memory text, embedding, and updated_at for an existing activated node.
// Used by the fine-mode dedup-merge pipeline when JudgeDedupMerge returns action="update".
// Args: $1 = memory_id (properties->>(('id'::text))), $2 = new memory text, $3 = new embedding (text cast to vector(1024)), $4 = updated_at (text)
const UpdateMemoryNodeFull = `
UPDATE %[1]s."Memory"
SET properties = ag_catalog.agtype_in(
        (properties::text::jsonb || jsonb_build_object(
            'memory',     $2::text,
            'updated_at', $4::text
        ))::text
    ),
    embedding = CASE WHEN $3::text = '' THEN embedding ELSE $3::vector(1024) END
WHERE id = $1
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
RETURNING id AS node_id`
