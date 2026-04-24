package queries

// queries_search_fulltext.go — fulltext (tsvector) search SQL constants.
// Covers: FulltextSearch, FulltextSearchWithCutoff.

// FulltextSearch performs tsvector fulltext search on the properties_tsvector_zh column.
// The tsquery parameter $1 should be a properly formatted tsquery string for the 'simple'
// configuration (e.g. 'word1 & word2'). Results are ranked by ts_rank.
//
// Args: $1 = tsquery string (text),
//
//	$2 = user_name (text),
//	$3 = user_id (text),
//	$4 = memory_types (text[]),
//	$5 = limit (int),
//	$6 = agent_id (text, '' for any)
//
// Returns the stable property UUID (properties->>'id'), NOT the AGE graphid —
// callers mix this ID with write-path / handler code which store property UUIDs.
const FulltextSearch = `
SELECT properties->>(('id'::text)) AS memory_id,
       (properties::text::jsonb - 'sources')::text,
       ts_rank(properties_tsvector_zh, to_tsquery('simple', $1)) AS rank
FROM %[1]s."Memory"
WHERE properties_tsvector_zh @@ to_tsquery('simple', $1)
  AND properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('user_id'::text))   = $3
  AND properties->>(('memory_type'::text)) = ANY($4)
  AND ($6::text = '' OR properties->>(('agent_id'::text)) = $6)
ORDER BY rank DESC
LIMIT $5`

// FulltextSearchWithCutoff is FulltextSearch with an additional created_at filter for temporal scope.
//
// Args: $1 = tsquery string (text),
//
//	$2 = user_name (text),
//	$3 = user_id (text),
//	$4 = memory_types (text[]),
//	$5 = limit (int),
//	$6 = cutoff ISO timestamp (text),
//	$7 = agent_id (text, '' for any)
const FulltextSearchWithCutoff = `
SELECT properties->>(('id'::text)) AS memory_id,
       (properties::text::jsonb - 'sources')::text,
       ts_rank(properties_tsvector_zh, to_tsquery('simple', $1)) AS rank
FROM %[1]s."Memory"
WHERE properties_tsvector_zh @@ to_tsquery('simple', $1)
  AND properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('user_id'::text))   = $3
  AND properties->>(('memory_type'::text)) = ANY($4)
  AND ($7::text = '' OR properties->>(('agent_id'::text)) = $7)
  AND (properties->>(('created_at'::text))) >= $6
ORDER BY rank DESC
LIMIT $5`
