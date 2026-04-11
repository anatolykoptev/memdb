package queries

// queries_memory_admin.go — SQL queries for admin/filter operations.
// Covers: filter-based GET, raw memory detection, admin reprocess.

// GetMemoriesByFilterSQL is a SQL template for fetching memories matching user_name
// conditions (OR-joined) AND filter conditions (AND-joined) with a LIMIT clause.
// The WHERE template takes no $N parameters — all conditions are inlined by the caller
// using the filter package (which escapes values at render time). The LIMIT value is
// also inlined (integer, validated to ≤1000 by the handler).
//
// Usage: fmt.Sprintf(GetMemoriesByFilterSQL, graphName, whereSQLLiteral, limitInt)
const GetMemoriesByFilterSQL = `
SELECT properties::text
FROM %[1]s."Memory"
WHERE %[2]s
LIMIT %[3]d`

// FindRawMemories returns activated LTM/UserMemory nodes that contain raw conversation
// patterns (role: [timestamp]: content). Used by the admin reprocess endpoint.
// Args: $1 = user_name, $2 = memory_types (text[]), $3 = limit
const FindRawMemories = `
SELECT properties->>'id'     AS prop_id,
       properties->>'memory' AS memory
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'status' = 'activated'
  AND position('assistant: [20' IN properties->>'memory') > 0
ORDER BY (properties->>'created_at') ASC NULLS LAST
LIMIT $3`

// CountRawMemories returns the total count of raw conversation-window memories.
// Args: $1 = user_name, $2 = memory_types (text[])
const CountRawMemories = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'status' = 'activated'
  AND position('assistant: [20' IN properties->>'memory') > 0`
