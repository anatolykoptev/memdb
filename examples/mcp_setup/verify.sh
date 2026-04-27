#!/usr/bin/env bash
# Verify the local memdb-mcp server is up and lists tools.
#
# Usage:
#   bash verify.sh                # default http://127.0.0.1:8001
#   MCP_URL=http://host:8001 bash verify.sh

set -euo pipefail

MCP_BASE="${MCP_URL:-http://127.0.0.1:8001}"
HEALTH_URL="${MCP_BASE}/health"
RPC_URL="${MCP_BASE}/mcp"

echo "1. Probing ${HEALTH_URL}"
if ! curl -fsS --max-time 5 "${HEALTH_URL}" >/dev/null; then
    echo "  FAIL — memdb-mcp is not reachable. Start the stack:"
    echo "    cd ~/deploy/krolik-server && docker compose up -d"
    exit 1
fi
echo "  ok"

echo
echo "2. Calling tools/list at ${RPC_URL}"
# MCP streamable HTTP transport requires both content types in Accept.
RESP=$(curl -fsS --max-time 10 \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -X POST "${RPC_URL}" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}')

# Strip SSE framing ("event: message\ndata: {...}") to get the JSON payload.
JSON=$(echo "${RESP}" | sed -n 's/^data: //p' | head -1)
[ -z "${JSON}" ] && JSON="${RESP}"

# Try jq if available, else fall back to grep.
if command -v jq >/dev/null 2>&1; then
    echo "${JSON}" | jq -r '.result.tools[]?.name // empty' | sed 's/^/  - /'
else
    echo "${JSON}" | grep -oE '"name":"[^"]+"' | cut -d'"' -f4 | sed 's/^/  - /'
fi

echo
echo "Done. If you see tool names above, registration in your client should succeed."
