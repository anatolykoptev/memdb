package queries

// queries_memory_users.go — SQL queries for user/cube identity lookups.
// Covers: list users, count users, cube tag lookup, user existence check.

// ListUsers returns distinct user names (cube slot) from activated memories.
const ListUsers = `
SELECT DISTINCT properties->>(('user_name'::text)) AS user_name
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) IS NOT NULL
ORDER BY user_name`

// ListDistinctUserIDs returns distinct person identities (user_id slot) with first-seen time.
// Phase 2: uses properties->>(('user_id'::text)) (person slot), not user_name (cube slot).
const ListDistinctUserIDs = `
SELECT properties->>(('user_id'::text)) AS user_id, MIN(created_at) AS first_seen
FROM %[1]s."Memory"
WHERE properties->>(('user_id'::text)) IS NOT NULL
GROUP BY properties->>(('user_id'::text))
ORDER BY first_seen ASC`

// CountDistinctUsers returns the number of distinct user names with activated memories.
const CountDistinctUsers = `
SELECT COUNT(DISTINCT properties->>(('user_name'::text)))
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'`

// ListCubesByTag returns distinct cube IDs (user_name in node properties)
// where at least one activated memory has the given tag in properties->('tags'::text).
// Used by go-wowa to hydrate its knownCubes set at startup — it asks for
// cubes tagged "mode:raw" (experience memory marker).
//
// Args: $1 = tag (text)
const ListCubesByTag = `
SELECT DISTINCT properties->>(('user_name'::text)) AS cube_id
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) IS NOT NULL
  AND properties->('tags'::text) @> to_jsonb(ARRAY[$1]::text[])
ORDER BY cube_id`

// ExistUser checks if a user has any activated memories.
// Args: $1 = user_name (text)
const ExistUser = `
SELECT COUNT(*) > 0
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'
LIMIT 1`
