#!/usr/bin/env bash
# probe_empty_cube_search.sh — manual verification tool for the empty-cube
# near-duplicate bug. Not run by CI. Execute after wiping a cube to confirm
# the DB actually sees zero rows before re-running the smoke test.
#
# Usage: ./probe_empty_cube_search.sh [cube_id]
# Default cube_id: conv-26__speaker_a

set -euo pipefail

CUBE_ID="${1:-conv-26__speaker_a}"

echo ""
echo "=== probe 1: count rows matching cube filter (must be 0 for empty cube) ==="
docker exec postgres psql -U memos -d memos -c "
SELECT count(*) AS rows_with_cube_filter FROM memos_graph.\"Memory\"
WHERE properties::text::jsonb->>'status' = 'activated'
  AND properties::text::jsonb->>'user_name' = '${CUBE_ID}'
  AND properties::text::jsonb->>'user_id'   = '${CUBE_ID}'
  AND properties::text::jsonb->>'memory_type' IN ('LongTermMemory','UserMemory');
"

echo ""
echo "=== probe 2: vector search WITH cube filter (should return 0 rows on empty cube) ==="
docker exec postgres psql -U memos -d memos -c "
SELECT properties::text::jsonb->>'user_name' AS un,
       1 - (embedding::halfvec(1024) <=> ('[' || rtrim(repeat('0,', 1024), ',') || ']')::halfvec(1024)) AS score,
       properties::text::jsonb->>'memory_type' AS mt
FROM memos_graph.\"Memory\"
WHERE properties::text::jsonb->>'status' = 'activated'
  AND properties::text::jsonb->>'user_name' = '${CUBE_ID}'
  AND properties::text::jsonb->>'user_id'   = '${CUBE_ID}'
  AND properties::text::jsonb->>'memory_type' IN ('LongTermMemory','UserMemory')
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> ('[' || rtrim(repeat('0,', 1024), ',') || ']')::halfvec(1024) ASC
LIMIT 5;
"

echo ""
echo "=== probe 3: vector search WITHOUT cube filter (should return rows from other cubes) ==="
docker exec postgres psql -U memos -d memos -c "
SELECT properties::text::jsonb->>'user_name' AS un,
       1 - (embedding::halfvec(1024) <=> ('[' || rtrim(repeat('0,', 1024), ',') || ']')::halfvec(1024)) AS score
FROM memos_graph.\"Memory\"
WHERE properties::text::jsonb->>'status' = 'activated'
  AND properties::text::jsonb->>'memory_type' IN ('LongTermMemory','UserMemory')
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> ('[' || rtrim(repeat('0,', 1024), ',') || ']')::halfvec(1024) ASC
LIMIT 5;
"
