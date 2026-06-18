#!/usr/bin/env bash
# Live smoke: prove gemini_kali (or PENTEST_SANDBOX_IMAGE) can reach the pentest
# daemon MCP endpoint and upsert a project fact.
set -euo pipefail

DAEMON_URL="${PENTEST_DAEMON_URL:-http://127.0.0.1:8787}"
IMAGE="${PENTEST_SANDBOX_IMAGE:-gemini_kali-gemini-kali:latest}"
CONTAINER_CLI="${PENTEST_CONTAINER_CLI:-docker}"
FACT_KEY="${PENTEST_SMOKE_FACT_KEY:-live:sandbox-smoke}"

echo "==> checking daemon health at ${DAEMON_URL}"
health="$(curl -sf "${DAEMON_URL}/health")"
echo "${health}" | python3 -m json.tool

project_id="$(curl -sf -X POST "${DAEMON_URL}/api/projects" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Sandbox MCP Live Smoke","scope":{"domains":["example.com"]}}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
echo "==> created smoke project ${project_id}"

payload="$(python3 - <<PY
import json
print(json.dumps({
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "upsert_project_fact",
    "arguments": {
      "project_id": "${project_id}",
      "fact_key": "${FACT_KEY}",
      "category": "recon",
      "summary": "sandbox container reached trusted MCP",
      "body": "written by scripts/smoke-sandbox-mcp-live.sh",
      "confidence": "confirmed",
      "scope_status": "in_scope",
    },
  },
}))
PY
)"

mcp_port="$(python3 -c "from urllib.parse import urlparse; u=urlparse('${DAEMON_URL}'); print(u.port or 8787)")"
mcp_url="http://host.docker.internal:${mcp_port}/mcp"

echo "==> calling upsert_project_fact from ${IMAGE} via ${mcp_url}"
"${CONTAINER_CLI}" run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${IMAGE}" \
  curl -sf -X POST "${mcp_url}" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "${payload}"

echo
echo "==> verifying fact via REST API"
curl -sf "${DAEMON_URL}/api/projects/${project_id}/facts/${FACT_KEY}" | python3 -m json.tool

echo "OK: sandbox MCP live smoke passed"