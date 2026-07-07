#!/usr/bin/env python3
"""Launch real Codex / Claude / Pi tasks that must upsert a fact via trusted MCP."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
TIMEOUT_SEC = int(os.environ.get("PENTEST_SMOKE_TIMEOUT_SEC", "300"))
STALL_SEC = int(os.environ.get("PENTEST_SMOKE_STALL_SEC", "90"))
STARTUP_SEC = int(os.environ.get("PENTEST_SMOKE_STARTUP_SEC", "45"))
POLL_SEC = 3
PREFLIGHT_TIMEOUT_SEC = int(os.environ.get("PENTEST_SMOKE_PREFLIGHT_TIMEOUT_SEC", "90"))

SMOKE_GOAL = (
    "SMOKE TEST: call trusted MCP tool upsert_project_fact once, then exit. "
    "Use project_id from PENTEST_PROJECT_ID. "
    "fact_key={fact_key} category=recon summary={summary} "
    "body=runtime task-level MCP smoke confidence=confirmed scope_status=in_scope. "
    "No shell work."
)

# Claude + third-party Anthropic-compatible APIs (e.g. Zhipu GLM) may not expose
# local MCP tools in -p mode; allow one JSON-RPC call to PENTEST_MCP_URL as fallback.
CLAUDE_SMOKE_GOAL = (
    "Authorized automated smoke test. Do not ask questions. "
    "Call upsert_project_fact once (or one curl JSON-RPC to PENTEST_MCP_URL). "
    "project_id from PENTEST_PROJECT_ID. "
    "fact_key={fact_key} category=recon summary={summary} "
    "body=runtime task-level MCP smoke confidence=confirmed scope_status=in_scope. "
    "Then exit."
)

FATAL_OUTPUT_PATTERNS = (
    "not logged in",
    "please run /login",
    "invalid api key",
    "incorrect api key",
    "authentication",
    "unauthorized",
    "permission denied",
    "runtime process failed",
    "preflight failed",
    "connection refused",
    "name or service not known",
    "no such file or directory",
    "executable file not found",
)


def request(method: str, path: str, body: dict | None = None) -> dict:
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("PENTEST_AUTH_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(
        DAEMON + path,
        data=data,
        method=method,
        headers=headers,
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read().decode()
        return json.loads(raw) if raw else {}


def load_secret_env(name: str) -> str | None:
    value = os.environ.get(name, "").strip()
    if value:
        return value
    return None


def has_codex_auth() -> bool:
    if load_secret_env("OPENAI_API_KEY"):
        return True
    auth_path = Path.home() / ".codex" / "auth.json"
    if not auth_path.exists():
        return False
    data = json.loads(auth_path.read_text())
    if (data.get("OPENAI_API_KEY") or "").strip():
        return True
    tokens = data.get("tokens")
    return isinstance(tokens, dict) and bool((tokens.get("access_token") or "").strip())


def load_pi_api_key() -> str | None:
    auth_path = Path.home() / ".pi" / "agent" / "auth.json"
    if auth_path.exists():
        data = json.loads(auth_path.read_text())
        for provider in ("xiaomi-token-plan-cn", "openai-codex", "openai", "anthropic"):
            entry = data.get(provider)
            if isinstance(entry, dict):
                key = (entry.get("key") or "").strip()
                if key:
                    return key
        for entry in data.values():
            if isinstance(entry, dict):
                key = (entry.get("key") or "").strip()
                if key:
                    return key
    return load_secret_env("ANTHROPIC_API_KEY") or load_secret_env("OPENAI_API_KEY")


def upsert_profile(name: str, provider: str, fields: dict) -> str:
    profiles = request("GET", "/api/runtime-profiles").get("profiles", [])
    for profile in profiles:
        if profile.get("name") == name and profile.get("provider") == provider:
            request(
                "PATCH",
                f"/api/runtime-profiles/{profile['id']}",
                {"name": name, "provider": provider, "fields": fields},
            )
            return profile["id"]
    created = request(
        "POST",
        "/api/runtime-profiles",
        {"name": name, "provider": provider, "fields": fields},
    )
    return created["id"]


def event_text(event: dict) -> str:
    payload = event.get("payload") or {}
    for key in ("text", "message", "error"):
        value = payload.get(key)
        if value:
            return str(value)
    return ""


def fatal_output(text: str) -> str | None:
    lowered = text.lower()
    for pattern in FATAL_OUTPUT_PATTERNS:
        if pattern in lowered:
            return text.strip()[:300]
    return None


def format_event_tail(events: list[dict], limit: int = 8) -> list[str]:
    tail: list[str] = []
    for event in events[-limit:]:
        text = event_text(event)
        if text:
            tail.append(text[:240])
    return tail


def wait_for_smoke_outcome(project_id: str, task_id: str, fact_key: str) -> tuple[bool, str]:
    """Poll fact + task status together so failures exit quickly instead of hanging."""
    deadline = time.time() + TIMEOUT_SEC
    started_at = time.time()
    seen_events = 0
    last_activity = started_at
    process_started = False
    encoded_fact = urllib.request.quote(fact_key, safe="")

    while time.time() < deadline:
        try:
            fact = request("GET", f"/api/projects/{project_id}/facts/{encoded_fact}")
            if fact.get("fact_key") == fact_key:
                print(f"  fact ok: {fact.get('summary')}")
                return True, "fact"
        except urllib.error.HTTPError as err:
            if err.code != 404:
                raise

        task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
        status = task.get("status", "")

        events = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/events").get(
            "events", []
        )
        if len(events) > seen_events:
            for event in events[seen_events:]:
                text = event_text(event)
                if text:
                    last_activity = time.time()
                    fatal = fatal_output(text)
                    if fatal:
                        return False, f"runtime error: {fatal}"
                payload = event.get("payload") or {}
                if event.get("kind") == "lifecycle" and payload.get("phase") == "process_started":
                    process_started = True
                    last_activity = time.time()
            seen_events = len(events)

        if status in {"failed", "stopped"}:
            return False, status

        if status == "completed":
            return False, "completed_without_fact"

        now = time.time()
        if status == "pending" and now - started_at > STARTUP_SEC:
            return False, f"stuck_pending>{STARTUP_SEC}s"

        if process_started and now - last_activity > STALL_SEC:
            return False, f"stalled>{STALL_SEC}s_no_output"

        time.sleep(POLL_SEC)

    return False, f"timeout>{TIMEOUT_SEC}s"


def preflight_claude_sandbox(image: str, endpoint: str, token: str, model: str = "glm-5.2") -> str | None:
    """Quick auth probe before launching the full MCP smoke task."""
    if os.environ.get("PENTEST_SMOKE_SKIP_PREFLIGHT", "").strip():
        return None

    cmd = [
        "docker",
        "run",
        "--rm",
        "-i",
        "--add-host=host.docker.internal:host-gateway",
        "-e",
        f"ANTHROPIC_AUTH_TOKEN={token}",
        "-e",
        f"ANTHROPIC_BASE_URL={endpoint}",
        "-e",
        f"ANTHROPIC_MODEL={model}",
        "-e",
        "IS_SANDBOX=1",
        "-e",
        "CLAUDE_HOME=/tmp/claude-smoke-preflight",
        image,
        "claude",
        "-p",
        "--dangerously-skip-permissions",
        "--model",
        model,
        "reply exactly: pong",
    ]
    try:
        proc = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=PREFLIGHT_TIMEOUT_SEC,
            check=False,
        )
    except subprocess.TimeoutExpired:
        return (
            f"claude preflight timed out after {PREFLIGHT_TIMEOUT_SEC}s "
            f"(endpoint={endpoint})"
        )

    combined = (proc.stdout + "\n" + proc.stderr).strip()
    fatal = fatal_output(combined)
    if proc.returncode != 0 or fatal:
        detail = fatal or combined[:300] or f"exit {proc.returncode}"
        return f"claude preflight failed: {detail}"
    if "pong" not in combined.lower():
        return f"claude preflight unexpected output: {combined[:300]}"
    print("  preflight ok: zhipu claude auth + sandbox")
    return None


def launch_case(
    project_id: str,
    provider: str,
    profile_id: str,
    runner: str,
    fact_key: str,
    summary: str,
    *,
    goal_template: str = SMOKE_GOAL,
    preflight: str | None = None,
) -> bool:
    print(f"\n==> {provider} ({runner})")
    if preflight:
        print(f"  FAIL preflight: {preflight}")
        return False

    goal = goal_template.format(fact_key=fact_key, summary=summary)
    created = request(
        "POST",
        f"/api/projects/{project_id}/tasks",
        {
            "goal": goal,
            "runtime_profile_id": profile_id,
            "runner": runner,
        },
    )
    task_id = created["id"]
    print(f"  task {task_id} status={created.get('status')}")

    ok, reason = wait_for_smoke_outcome(project_id, task_id, fact_key)
    if ok:
        return True

    events = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/events").get(
        "events", []
    )
    tail = format_event_tail(events)
    print(f"  task ended: {reason}")
    if tail:
        print("  recent output:")
        for line in tail:
            print(f"    {line}")
    return False


def wait_for_daemon() -> dict:
    deadline = time.time() + 30
    last_err: Exception | None = None
    while time.time() < deadline:
        try:
            return request("GET", "/health")
        except (urllib.error.URLError, TimeoutError) as err:
            last_err = err
            time.sleep(1)
    raise RuntimeError(f"daemon not reachable at {DAEMON}: {last_err}")


def main() -> int:
    print(f"daemon: {DAEMON}")
    health = wait_for_daemon()
    runner_info = health.get("runner", {})
    sandbox_image = runner_info.get("sandbox_image", "")
    print(f"sandbox image: {sandbox_image}")
    print(
        "timeouts: "
        f"total={TIMEOUT_SEC}s stall={STALL_SEC}s startup={STARTUP_SEC}s "
        f"preflight={PREFLIGHT_TIMEOUT_SEC}s"
    )

    anthropic = load_secret_env("ANTHROPIC_AUTH_TOKEN") or load_secret_env("ANTHROPIC_API_KEY")
    codex_auth = has_codex_auth()
    pi_key = load_pi_api_key()
    missing = []
    if not anthropic:
        missing.append("ANTHROPIC_AUTH_TOKEN")
    if not codex_auth:
        missing.append("Codex auth (~/.codex/auth.json or OPENAI_API_KEY)")
    if not pi_key:
        missing.append("PI API key")
    if missing:
        print("missing credentials:", ", ".join(missing), file=sys.stderr)
        return 2

    zhipu_endpoint = os.environ.get(
        "PENTEST_ZHIPU_ANTHROPIC_URL",
        "https://open.bigmodel.cn/api/anthropic",
    ).strip()

    project = request(
        "POST",
        "/api/projects",
        {
            "name": "Runtime MCP Live Smoke",
            "scope": {"domains": ["example.com"], "notes": "automated runtime smoke"},
        },
    )
    project_id = project["id"]
    print(f"project: {project_id}")

    codex_fields = {
        "model": "gpt-5.5",
        "default_runner": "sandbox",
        "env": {"PENTEST_CODEX_SUBCOMMAND": "exec"},
        "custom_args": ["--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"],
    }
    openai = load_secret_env("OPENAI_API_KEY")
    if openai:
        codex_fields["api_keys"] = {"OPENAI_API_KEY": openai}
    codex_profile = upsert_profile("Smoke Codex", "codex", codex_fields)
    claude_profile = upsert_profile(
        "Smoke Claude",
        "claude_code",
        {
            "model": "glm-5.2",
            "endpoint": zhipu_endpoint,
            "default_runner": "sandbox",
            "env": {"ANTHROPIC_BASE_URL": zhipu_endpoint},
            "custom_args": [
                "-p",
                "--dangerously-skip-permissions",
                "--permission-mode",
                "bypassPermissions",
            ],
            "api_keys": {"ANTHROPIC_AUTH_TOKEN": anthropic},
        },
    )
    pi_profile = upsert_profile(
        "Smoke Pi",
        "pi",
        {
            "model": "mimo-v2.5-pro",
            "default_runner": "host",
            "env": {
                "PI_PROVIDER_ID": "xiaomi-token-plan-cn",
                "PI_API": "openai-completions",
                "PENTEST_PI_NPM_VERSION": "0.78.0",
            },
            "custom_args": ["-p", "--provider", "xiaomi-token-plan-cn"],
            "binary_path": "pi",
        },
    )

    claude_preflight_error = None
    if sandbox_image:
        claude_preflight_error = preflight_claude_sandbox(
            sandbox_image,
            zhipu_endpoint,
            anthropic,
        )

    cases = [
        ("codex", codex_profile, "sandbox", "smoke:codex", "Codex runtime MCP smoke passed", SMOKE_GOAL, None),
        (
            "claude_code",
            claude_profile,
            "sandbox",
            "smoke:claude",
            "Claude runtime MCP smoke passed",
            CLAUDE_SMOKE_GOAL,
            claude_preflight_error,
        ),
        ("pi", pi_profile, "host", "smoke:pi", "Pi runtime MCP smoke passed", SMOKE_GOAL, None),
        (
            "pi_sandbox",
            pi_profile,
            "sandbox",
            "smoke:pi-sandbox",
            "Pi sandbox runtime MCP smoke passed",
            SMOKE_GOAL,
            None,
        ),
    ]
    only = [part.strip() for part in os.environ.get("PENTEST_SMOKE_ONLY", "").split(",") if part.strip()]
    if only:
        cases = [case for case in cases if case[0] in only]

    results: dict[str, bool] = {}
    for provider, profile_id, runner, fact_key, summary, goal_template, preflight in cases:
        try:
            results[provider] = launch_case(
                project_id,
                provider,
                profile_id,
                runner,
                fact_key,
                summary,
                goal_template=goal_template,
                preflight=preflight,
            )
        except Exception as err:  # noqa: BLE001
            print(f"  error: {err}", file=sys.stderr)
            results[provider] = False

    print("\n==> summary")
    ok = True
    for provider, passed in results.items():
        mark = "PASS" if passed else "FAIL"
        print(f"  {provider}: {mark}")
        ok = ok and passed

    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())