package queries

// queries_memory_users.go — SQL queries for user/cube identity lookups.
// Covers: list users, count users, cube tag lookup, user existence check.

// ListUsers returns distinct user names (cube slot) from activated memories.
const ListUsers = `
SELECT DISTINCT properties->>'user_name' AS user_name
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' IS NOT NULL
ORDER BY user_name`

// ListDistinctUserIDs returns distinct person identities (user_id slot) with first-seen time.
// Phase 2: uses properties->>'user_id' (person slot), not user_name (cube slot).
const ListDistinctUserIDs = `
SELECT properties->>'user_id' AS user_id, MIN(created_at) AS first_seen
FROM %[1]s."Memory"
WHERE properties->>'user_id' IS NOT NULL
GROUP BY properties->>'user_id'
ORDER BY first_seen ASC`

// CountDistinctUsers returns the number of distinct user names with activated memories.
const CountDistinctUsers = `
SELECT COUNT(DISTINCT properties->>'user_name')
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'`

// ListCubesByTag returns distinct cube IDs (user_name in node properties)
// where at least one activated memory has the given tag in properties->'tags'.
// Used by go-wowa to hydrate its knownCubes set at startup — it asks for
// cubes tagged "mode:raw" (experience memory marker).
//
// Args: $1 = tag (text)
const ListCubesByTag = `
SELECT DISTINCT properties->>'user_name' AS cube_id
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' IS NOT NULL
  AND properties->'tags' @> to_jsonb(ARRAY[$1]::text[])
ORDER BY cube_id`

// ExistUser checks if a user has any activated memories.
// Args: $1 = user_name (text)
const ExistUser = `
SELECT COUNT(*) > 0
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'
LIMIT 1`
