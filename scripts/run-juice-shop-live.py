#!/usr/bin/env python3
"""Launch a real pentest task against local OWASP Juice Shop (Score Board challenge)."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
JUICE_URL = os.environ.get("PENTEST_JUICE_URL", "http://127.0.0.1:3000").rstrip("/")
JUICE_HOST = JUICE_URL.replace("https://", "").replace("http://", "").split("/")[0]
TIMEOUT_SEC = int(os.environ.get("PENTEST_JUICE_TIMEOUT_SEC", "1800"))
POLL_SEC = 5

GOAL = f"""Authorized pentest of OWASP Juice Shop at {JUICE_URL} (in scope only).

Objectives:
1. Recon the web app (agent-browser or curl). Map login, API, and client-side assets.
2. Find the hidden **Score Board** challenge (Juice Shop score board / tutorial hub).
3. Record recon facts and at least one finding via trusted pentest MCP tools:
   upsert_project_fact, upsert_finding, attach_evidence when you have proof.
4. Prefer sandbox tooling (nuclei, ffuf, agent-browser). Stay in scope: {JUICE_HOST} only.
5. When Score Board is located, upsert a fact key juice-shop:score-board with summary describing how to access it.

Do not ask questions. Execute and use MCP tools for all blackboard writes."""


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
    with urllib.request.urlopen(req, timeout=60) as resp:
        raw = resp.read().decode()
        return json.loads(raw) if raw else {}


def ensure_juice_shop() -> None:
    try:
        urllib.request.urlopen(JUICE_URL, timeout=5)
        print(f"juice shop reachable: {JUICE_URL}")
        return
    except Exception:
        pass
    print("starting juice-shop container...")
    subprocess.run(
        [
            "docker",
            "run",
            "-d",
            "--rm",
            "--name",
            "pentest-juice-shop",
            "-p",
            "3000:3000",
            "bkimminich/juice-shop",
        ],
        check=True,
    )
    deadline = time.time() + 120
    while time.time() < deadline:
        try:
            urllib.request.urlopen(JUICE_URL, timeout=3)
            print(f"juice shop ready: {JUICE_URL}")
            return
        except Exception:
            time.sleep(2)
    raise RuntimeError(f"juice shop not ready at {JUICE_URL}")


def upsert_profile(name: str, provider: str, fields: dict) -> str:
    profiles = request("GET", "/api/runtime-profiles").get("profiles", [])
    for p in profiles:
        if p.get("name") == name and p.get("provider") == provider:
            request("PATCH", f"/api/runtime-profiles/{p['id']}", {"name": name, "provider": provider, "fields": fields})
            return p["id"]
    created = request("POST", "/api/runtime-profiles", {"name": name, "provider": provider, "fields": fields})
    return created["id"]


def score_board_fact(facts: list) -> dict | None:
    for fact in facts:
        key = (fact.get("fact_key") or "").lower()
        if "score-board" in key or key.endswith(":score-board"):
            return fact
    return None


def wait_for_outcome(project_id: str, task_id: str) -> tuple[bool, str]:
    start = time.time()
    last_status = ""
    while time.time() - start < TIMEOUT_SEC:
        task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
        status = task.get("status", "")
        if status != last_status:
            print(f"  task status: {status}")
            last_status = status

        facts = request("GET", f"/api/projects/{project_id}/facts/index").get("facts", [])
        hit = score_board_fact(facts)
        if hit:
            return True, f"score-board fact: {hit.get('fact_key')}"

        if status in ("completed", "failed", "stopped"):
            findings = request("GET", f"/api/projects/{project_id}/findings").get("findings", [])
            if findings:
                return True, f"task {status} with {len(findings)} finding(s)"
            return status == "completed", f"task {status}, facts={len(facts)}"
        time.sleep(POLL_SEC)
    facts = request("GET", f"/api/projects/{project_id}/facts/index").get("facts", [])
    hit = score_board_fact(facts)
    if hit:
        return True, f"score-board fact (after timeout): {hit.get('fact_key')}"
    return False, "timeout"


def main() -> int:
    ensure_juice_shop()
    health = request("GET", "/health")
    print(f"daemon: {DAEMON} sandbox={health.get('runner', {}).get('sandbox_image')}")

    anthropic = os.environ.get("ANTHROPIC_AUTH_TOKEN") or os.environ.get("ANTHROPIC_API_KEY")
    if not anthropic:
        print("missing ANTHROPIC_AUTH_TOKEN for Claude/Zhipu profile", file=sys.stderr)
        return 2

    zhipu = os.environ.get("PENTEST_ZHIPU_ANTHROPIC_URL", "https://open.bigmodel.cn/api/anthropic").strip()
    profile_id = upsert_profile(
        "Juice Shop Claude",
        "claude_code",
        {
            "model": "glm-5.2",
            "endpoint": zhipu,
            "default_runner": "sandbox",
            "sandbox_image": os.environ.get("PENTEST_SANDBOX_IMAGE", "ghcr.io/n1majne3/cyberpenda-sandbox:latest"),
            "env": {"ANTHROPIC_BASE_URL": zhipu},
            "custom_args": [
                "-p",
                "--dangerously-skip-permissions",
                "--permission-mode",
                "bypassPermissions",
            ],
            "api_keys": {"ANTHROPIC_AUTH_TOKEN": anthropic},
        },
    )

    project = request(
        "POST",
        "/api/projects",
        {
            "name": "Juice Shop Score Board",
            "scope": {
                "urls": [JUICE_URL],
                "domains": [JUICE_HOST.split(":")[0]],
                "notes": "Local OWASP Juice Shop — Score Board challenge",
            },
        },
    )
    project_id = project["id"]
    print(f"project: {project_id}")

    task = request(
        "POST",
        f"/api/projects/{project_id}/tasks",
        {
            "goal": GOAL,
            "runtime_profile_id": profile_id,
            "runner": "sandbox",
            "run_controls": {"yolo": True},
        },
    )
    task_id = task["id"]
    print(f"task: {task_id}")

    ok, reason = wait_for_outcome(project_id, task_id)
    print("PASS" if ok else "FAIL", reason)
    print(f"UI: /projects/{project_id}/tasks/{task_id}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
