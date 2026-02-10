package queries

// --- Search Queries ---

// VectorSearch performs cosine similarity search across multiple memory types using pgvector.
// The vector parameter $1 must be a text string literal (e.g. '[0.1,0.2,...]') cast to
// vector(1024), because pgvector with Apache AGE does not accept native float arrays.
// The Go code is responsible for formatting the embedding as a bracket-delimited string.
//
// Args: $1 = vector string literal (text, cast to vector(1024)),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]),
//	$4 = limit (int)
const VectorSearch = `
SELECT id::text,
       properties::text,
       (1 - (embedding <=> $1::vector(1024))) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'memory_type' = ANY($3)
  AND embedding IS NOT NULL
ORDER BY score DESC
LIMIT $4`

// FulltextSearch performs tsvector fulltext search on the properties_tsvector_zh column.
// The tsquery parameter $1 should be a properly formatted tsquery string for the 'simple'
// configuration (e.g. 'word1 & word2'). Results are ranked by ts_rank.
//
// Args: $1 = tsquery string (text),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]),
//	$4 = limit (int)
const FulltextSearch = `
SELECT id::text,
       properties::text,
       ts_rank(properties_tsvector_zh, to_tsquery('simple', $1)) AS rank
FROM %[1]s."Memory"
WHERE properties_tsvector_zh @@ to_tsquery('simple', $1)
  AND properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'memory_type' = ANY($3)
ORDER BY rank DESC
LIMIT $4`
