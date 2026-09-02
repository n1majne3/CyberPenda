#!/usr/bin/env bash
# Live smoke: prove the CyberPenda sandbox image (or PENTEST_SANDBOX_IMAGE)
# can reach the daemon Blackboard v2 HTTP semantic-record boundary.
set -euo pipefail

DAEMON_URL="${PENTEST_DAEMON_URL:-http://127.0.0.1:8787}"
IMAGE="${PENTEST_SANDBOX_IMAGE:-ghcr.io/n1majne3/cyberpenda-sandbox:latest}"
CONTAINER_CLI="${PENTEST_CONTAINER_CLI:-docker}"
SEMANTIC_RECORD_KEY="${PENTEST_SMOKE_RECORD_KEY:-live:sandbox-smoke}"
SEMANTIC_CHANGE_IDEMPOTENCY_KEY="${PENTEST_SMOKE_IDEMPOTENCY_KEY:-sandbox-http-live}"

# Curl auth header args, empty when no operator token is configured (loopback dev).
auth_args=()
if [[ -n "${PENTEST_AUTH_TOKEN:-}" ]]; then
  auth_args+=(-H "Authorization: Bearer ${PENTEST_AUTH_TOKEN}")
fi

echo "==> checking daemon health at ${DAEMON_URL}"
health="$(curl -sf "${DAEMON_URL}/health")"
echo "${health}" | python3 -m json.tool

project_id="$(curl -sf -X POST "${DAEMON_URL}/api/projects" \
  "${auth_args[@]+"${auth_args[@]}"}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Sandbox Blackboard HTTP Live Smoke","kind":"pentest","scope":{"domains":["example.com"]}}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
echo "==> created smoke project ${project_id}"

daemon_port="$(DAEMON_URL="${DAEMON_URL}" python3 -c 'import os; from urllib.parse import urlparse; u=urlparse(os.environ["DAEMON_URL"]); print(u.port or 8787)')"

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

v2_base_url="http://host.docker.internal:${daemon_port}/api/v2/projects/${project_id}"
v2_auth_args=()
if [[ -n "${PENTEST_AUTH_TOKEN:-}" ]]; then
  v2_auth_args+=(-H "Authorization: Bearer ${PENTEST_AUTH_TOKEN}")
fi

echo "==> POSTing a Blackboard v2 semantic change from ${IMAGE}"
change_response="$("${CONTAINER_CLI}" run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${IMAGE}" \
  curl -sf -X POST "${v2_base_url}/blackboard/changes" \
    "${v2_auth_args[@]+"${v2_auth_args[@]}"}" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: ${SEMANTIC_CHANGE_IDEMPOTENCY_KEY}" \
    -d "${semantic_change_payload}")"
echo "${change_response}" | python3 -m json.tool

echo "==> verifying semantic record via Blackboard v2 from ${IMAGE}"
semantic_record_response="$("${CONTAINER_CLI}" run --rm \
  --add-host=host.docker.internal:host-gateway \
  "${IMAGE}" \
  curl -sf -X GET "${v2_base_url}/blackboard/records/${SEMANTIC_RECORD_KEY}" \
    "${v2_auth_args[@]+"${v2_auth_args[@]}"}")"

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

echo "OK: sandbox Blackboard v2 HTTP live smoke passed"
