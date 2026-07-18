#!/usr/bin/env bash
# Live smoke: prove gemini_kali (or PENTEST_SANDBOX_IMAGE) can reach the pentest
# daemon MCP endpoint and the Blackboard v2 HTTP semantic-record boundary.
set -euo pipefail

DAEMON_URL="${PENTEST_DAEMON_URL:-http://127.0.0.1:8787}"
IMAGE="${PENTEST_SANDBOX_IMAGE:-gemini_kali-gemini-kali:latest}"
CONTAINER_CLI="${PENTEST_CONTAINER_CLI:-docker}"
SEMANTIC_RECORD_KEY="${PENTEST_SMOKE_RECORD_KEY:-live:sandbox-smoke}"
SEMANTIC_CHANGE_IDEMPOTENCY_KEY="${PENTEST_SMOKE_IDEMPOTENCY_KEY:-sandbox-mcp-live}"

# Curl auth header args, empty when no operator token is configured (loopback dev).
auth_args=()
if [[ -n "${PENTEST_AUTH_TOKEN:-}" ]]; then
  auth_args+=(-H "Authorization: Bearer ${PENTEST_AUTH_TOKEN}")
fi

echo "==> checking daemon health at ${DAEMON_URL}"
health="$(curl -sf "${DAEMON_URL}/health")"
echo "${health}" | python3 -m json.tool

project_id="$(curl -sf -X POST "${DAEMON_URL}/api/projects" \
  "${auth_args[@]}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Sandbox MCP Live Smoke","scope":{"domains":["example.com"]}}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
echo "==> created smoke project ${project_id}"

mcp_port="$(DAEMON_URL="${DAEMON_URL}" python3 -c 'import os; from urllib.parse import urlparse; u=urlparse(os.environ["DAEMON_URL"]); print(u.port or 8787)')"
mcp_url="http://host.docker.internal:${mcp_port}/mcp"
tools_list_payload='{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'

# tools/list is discovery only. Do not send the operator token to MCP: trusted
# MCP writes require a Continuation Grant and this smoke must not impersonate one.
echo "==> listing Blackboard v2 MCP tools from ${IMAGE} via ${mcp_url}"
mcp_response="$("${CONTAINER_CLI}" run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${IMAGE}" \
  curl -sf -X POST "${mcp_url}" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "${tools_list_payload}")"

MCP_RESPONSE="${mcp_response}" python3 - <<'PY'
import json
import os

raw = os.environ["MCP_RESPONSE"].strip()
messages = []
try:
    messages.append(json.loads(raw))
except json.JSONDecodeError:
    for line in raw.splitlines():
        if line.startswith("data:"):
            try:
                messages.append(json.loads(line[5:].strip()))
            except json.JSONDecodeError:
                pass

listed = next(
    (
        message
        for message in messages
        if isinstance(message, dict)
        and isinstance(message.get("result"), dict)
        and isinstance(message["result"].get("tools"), list)
    ),
    None,
)
if listed is None:
    raise SystemExit("tools/list response did not contain a result.tools list")

names = [tool.get("name") for tool in listed["result"]["tools"]]
want = {
    "blackboard_change",
    "blackboard_read",
    "blackboard_history",
    "blackboard_retain_evidence",
    "blackboard_checkpoint_attempt",
    "blackboard_finish",
}
if len(names) != len(want) or set(names) != want:
    raise SystemExit(f"unexpected Blackboard v2 MCP tools: {names!r}")
retired_tool = "upsert_" + "project_fact"
if retired_tool in names:
    raise SystemExit("a retired MCP tool is still advertised")
print("MCP tools/list contains exactly the six Blackboard v2 tools")
PY

semantic_change_payload="$(SEMANTIC_RECORD_KEY="${SEMANTIC_RECORD_KEY}" python3 - <<'PY'
import json
import os

print(json.dumps({
    "schema": "semantic-change-batch/v2",
    "changes": [{
        "op": "create",
        "key": os.environ["SEMANTIC_RECORD_KEY"],
        "type": "fact",
        "record": {
            "category": "recon",
            "summary": "sandbox container reached the Blackboard v2 HTTP boundary",
            "body": "written by scripts/smoke-sandbox-mcp-live.sh",
            "confidence": "confirmed",
            "scope_status": "in_scope",
        },
    }],
}, separators=(",", ":")))
PY
)"

v2_base_url="http://host.docker.internal:${mcp_port}/api/v2/projects/${project_id}"
v2_auth_args=()
if [[ -n "${PENTEST_AUTH_TOKEN:-}" ]]; then
  v2_auth_args+=(-H "Authorization: Bearer ${PENTEST_AUTH_TOKEN}")
fi

echo "==> POSTing a Blackboard v2 semantic change from ${IMAGE}"
change_response="$("${CONTAINER_CLI}" run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${IMAGE}" \
  curl -sf -X POST "${v2_base_url}/blackboard/changes" \
    "${v2_auth_args[@]}" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: ${SEMANTIC_CHANGE_IDEMPOTENCY_KEY}" \
    -d "${semantic_change_payload}")"
echo "${change_response}" | python3 -m json.tool

echo "==> verifying semantic record via Blackboard v2 from ${IMAGE}"
semantic_record_response="$("${CONTAINER_CLI}" run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${IMAGE}" \
  curl -sf -X GET "${v2_base_url}/blackboard/records/${SEMANTIC_RECORD_KEY}" \
    "${v2_auth_args[@]}")"

SEMANTIC_RECORD_RESPONSE="${semantic_record_response}" \
SEMANTIC_RECORD_KEY="${SEMANTIC_RECORD_KEY}" \
python3 - <<'PY'
import json
import os

record = json.loads(os.environ["SEMANTIC_RECORD_RESPONSE"])
expected_key = os.environ["SEMANTIC_RECORD_KEY"]
if record.get("schema") != "blackboard-record/v2":
    raise SystemExit(f"unexpected Blackboard record schema: {record.get('schema')!r}")
if record.get("key") != expected_key:
    raise SystemExit(f"Blackboard record key {record.get('key')!r} != {expected_key!r}")
if record.get("record", {}).get("summary") != "sandbox container reached the Blackboard v2 HTTP boundary":
    raise SystemExit("Blackboard v2 read did not return the smoke semantic record")
PY
echo "${semantic_record_response}" | python3 -m json.tool

echo "OK: sandbox MCP discovery and Blackboard v2 live smoke passed"
