#!/usr/bin/env python3
"""Fair one-off: assisted vs interactive on SEPARATE Juice Shop instances + projects.

Measures Juice Shop official challenge solves via GET /api/Challenges on each
instance before/after the Task (not CyberPenda blackboard proxies).

Assumptions:
- assisted instance at PENTEST_ASSISTED_JUICE_URL (default http://127.0.0.1:3001)
- interactive instance at PENTEST_INTERACTIVE_JUICE_URL (default http://127.0.0.1:3002)
- sandbox reaches host via host.docker.internal:<port>
"""

from __future__ import annotations

from datetime import datetime, timezone
import json
import os
import sys
import time
import urllib.error
import urllib.request

DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
PROFILE_ID = os.environ.get(
    "PENTEST_RUNTIME_PROFILE_ID", "b4c2e5d8cfce278732905c8616743209"
).strip()
RUNNER = os.environ.get("PENTEST_RUNNER", "sandbox").strip() or "sandbox"
TIMEOUT_SEC = int(os.environ.get("PENTEST_JUICE_TIMEOUT_SEC", "1500"))
POLL_SEC = float(os.environ.get("PENTEST_JUICE_POLL_SEC", "8"))
IDLE_STABLE_SEC = int(os.environ.get("PENTEST_IDLE_STABLE_SEC", "90"))

ASSISTED_JUICE = os.environ.get("PENTEST_ASSISTED_JUICE_URL", "http://127.0.0.1:3001").rstrip("/")
INTERACTIVE_JUICE = os.environ.get(
    "PENTEST_INTERACTIVE_JUICE_URL", "http://127.0.0.1:3002"
).rstrip("/")


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def log(msg: str) -> None:
    print(f"[{_now()}] {msg}", flush=True)


def request(method: str, path: str, body: dict | None = None, timeout: int = 90) -> dict:
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("PENTEST_AUTH_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(DAEMON + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            raw = response.read().decode()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as error:
        detail = error.read().decode()[:800]
        raise RuntimeError(f"{method} {path} HTTP {error.code}: {detail}") from error


def juice_host(url: str) -> str:
    return url.replace("https://", "").replace("http://", "").split("/")[0]


def sandbox_url(url: str) -> str:
    """Map host-local Juice URL to the hostname the sandbox container can reach."""
    host = juice_host(url)
    if host.startswith("127.0.0.1:") or host.startswith("localhost:"):
        port = host.split(":")[-1]
        return f"http://host.docker.internal:{port}"
    if host in {"127.0.0.1", "localhost"}:
        return "http://host.docker.internal:3000"
    return url


def challenges_snapshot(juice_url: str) -> dict:
    with urllib.request.urlopen(juice_url + "/api/Challenges", timeout=15) as resp:
        data = json.loads(resp.read().decode())
    items = data.get("data") if isinstance(data, dict) else data
    if not isinstance(items, list):
        items = []
    solved = [c for c in items if isinstance(c, dict) and c.get("solved")]
    return {
        "total": len(items),
        "solved_count": len(solved),
        "solved_names": sorted(c.get("name") or c.get("key") or "?" for c in solved),
        "solved_keys": sorted(c.get("key") or "" for c in solved),
        "score_board_solved": any(
            (c.get("key") == "scoreBoardChallenge" or c.get("name") == "Score Board")
            and c.get("solved")
            for c in items
            if isinstance(c, dict)
        ),
    }


def goal_for(mode: str, juice_url: str) -> str:
    sb = sandbox_url(juice_url)
    host = juice_host(sb)
    common = f"""Authorized pentest of OWASP Juice Shop at {sb} (in scope only: {host}).

Primary objective — maximize official Score Board challenge solves on THIS instance:
1. Open the hidden Score Board page in a way that actually registers the "Score Board"
   challenge as solved (browser to {sb}/#/score-board or equivalent, not only reading
   main.js). Confirm via GET {sb}/api/Challenges that scoreBoardChallenge.solved is true.
2. Then solve as many other easy/starred challenges as you can in this one Task session
   (e.g. Exposed Metrics /metrics, Confidential Document /ftp, Error Handling, robots.txt
   related, DOM XSS basics — only in-scope). Prefer real solve signals over write-ups.
3. After each solve, re-check /api/Challenges and keep a running solved count.
4. Stay in scope: {host} only. Do not attack other hosts/ports.

Work hard in this single Task. Do not ask the operator questions."""
    if mode == "assisted":
        return common + f"""

Assisted Blackboard mode rules:
- Use non-Blackboard tools only (curl, browser, shell). Do NOT call Blackboard / trusted
  pentest MCP persistence tools. Semantic writes are the Harness's job after your turn.
- Do NOT finish the Task or launch Objectives.
- When you have done a solid solve batch (or are blocked), end the Work Runtime Turn.
- If a Conclude turn asks for an Attempt, summarize solves with outcome and produced_targets
  as appropriate; no more testing during conclusion."""
    return common + f"""

Interactive Blackboard mode rules:
- Use trusted pentest MCP / Blackboard tools to record progress: at least
  fact key juice-shop:score-board when Score Board is solved/located, plus facts/findings
  for other solves with evidence when available.
- Prefer sandbox tooling. Do not ask questions — execute."""


def stop_task(project_id: str, task_id: str) -> None:
    try:
        request("POST", f"/api/projects/{project_id}/tasks/{task_id}/stop", {})
    except Exception as exc:  # noqa: BLE001
        log(f"stop {task_id}: {exc}")


def timeline_stats(project_id: str, task_id: str) -> dict:
    timeline = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/timeline")
    items = [i for i in (timeline.get("items") or []) if isinstance(i, dict)]
    tool_use = sum(1 for i in items if i.get("type") == "tool_use")
    tool_result = sum(1 for i in items if i.get("type") == "tool_result")
    texts = "\n".join(str(i.get("content", "")) for i in items if i.get("type") == "text")
    return {
        "tool_events": tool_use + tool_result,
        "tool_use": tool_use,
        "text_items": sum(1 for i in items if i.get("type") == "text"),
        "score_board_mentioned": "score-board" in texts.lower()
        or "score board" in texts.lower(),
    }


def run_mode(mode: str, juice_url: str) -> dict:
    sb = sandbox_url(juice_url)
    host_local = juice_host(juice_url)
    host_sb = juice_host(sb)
    domain_local = host_local.split(":")[0]
    domain_sb = host_sb.split(":")[0]

    before = challenges_snapshot(juice_url)
    log(f"{mode}: juice={juice_url} sandbox_target={sb} baseline_solved={before['solved_count']}")

    project = request(
        "POST",
        "/api/projects",
        {
            "name": f"Challenges {mode} {datetime.now().strftime('%H%M%S')}",
            "scope": {
                "urls": [juice_url, sb],
                "domains": list({domain_local, domain_sb}),
                "notes": f"Isolated {mode} instance for challenge-count compare",
            },
        },
    )
    project_id = project["id"]
    run_controls: dict
    if mode == "assisted":
        run_controls = {"blackboard_conclusion_mode": "assisted"}
    else:
        run_controls = {"yolo": True, "blackboard_conclusion_mode": "interactive"}

    task = request(
        "POST",
        f"/api/projects/{project_id}/tasks",
        {
            "goal": goal_for(mode, juice_url),
            "runtime_profile_id": PROFILE_ID,
            "runner": RUNNER,
            "run_controls": run_controls,
        },
    )
    task_id = task["id"]
    log(f"{mode}: project={project_id} task={task_id}")

    started = time.time()
    deadline = started + TIMEOUT_SEC
    last_solved = before["solved_count"]
    last_change = time.time()
    idle_since: float | None = None
    last_task: dict = {}

    while time.time() < deadline:
        last_task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
        status = last_task.get("status")
        activity = last_task.get("runtime_activity") or {}
        conclusion = last_task.get("blackboard_conclusion") or {}
        snap = challenges_snapshot(juice_url)
        if snap["solved_count"] != last_solved:
            log(
                f"{mode}: SOLVE +{snap['solved_count'] - last_solved} -> "
                f"{snap['solved_count']}/{snap['total']} {snap['solved_names']}"
            )
            last_solved = snap["solved_count"]
            last_change = time.time()
            idle_since = None

        live_idle = (
            status == "running"
            and activity.get("liveness") == "live"
            and activity.get("turn_activity") == "idle"
        )
        if live_idle:
            if idle_since is None:
                idle_since = time.time()
        else:
            idle_since = None

        # Assisted: conclusion clean after apply + idle is a natural stop if no new
        # solves for a while. Interactive: idle with no new solves for IDLE_STABLE_SEC.
        concluded = (
            mode == "assisted"
            and conclusion.get("state") == "clean"
            and isinstance(conclusion.get("applied_revision"), int)
            and conclusion.get("applied_revision", 0) > 0
            and live_idle
            and (time.time() - last_change) >= min(60, IDLE_STABLE_SEC)
        )
        interactive_settled = (
            mode == "interactive"
            and live_idle
            and idle_since is not None
            and (time.time() - idle_since) >= IDLE_STABLE_SEC
            and (time.time() - last_change) >= IDLE_STABLE_SEC
        )
        if status in {"completed", "failed", "stopped"}:
            log(f"{mode}: task terminal status={status}")
            break
        if concluded or interactive_settled:
            log(
                f"{mode}: settling stop (concluded={concluded} interactive_settled={interactive_settled}) "
                f"solved={snap['solved_count']}"
            )
            break

        log(
            f"{mode}: status={status} act={activity.get('liveness')}/{activity.get('turn_activity')} "
            f"concl={conclusion.get('state')} rev={conclusion.get('applied_revision')} "
            f"juice_solved={snap['solved_count']}/{snap['total']} "
            f"sb_challenge={snap['score_board_solved']}"
        )
        time.sleep(POLL_SEC)

    after = challenges_snapshot(juice_url)
    wall = round(time.time() - started, 1)
    stats = timeline_stats(project_id, task_id)
    stop_task(project_id, task_id)
    time.sleep(1)
    after_task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
    conclusion = after_task.get("blackboard_conclusion") or last_task.get("blackboard_conclusion") or {}

    newly = sorted(set(after["solved_names"]) - set(before["solved_names"]))
    return {
        "mode": mode,
        "juice_url": juice_url,
        "sandbox_target": sb,
        "project_id": project_id,
        "task_id": task_id,
        "wall_clock_sec": wall,
        "before": before,
        "after": after,
        "newly_solved": newly,
        "newly_solved_count": len(newly),
        "score_board_challenge_solved": after["score_board_solved"],
        "timeline": stats,
        "task_status": after_task.get("status"),
        "conclusion_state": conclusion.get("state"),
        "applied_revision": conclusion.get("applied_revision"),
        "liveness_final": (after_task.get("runtime_activity") or {}).get("liveness"),
    }


def main() -> int:
    if not PROFILE_ID:
        print("PENTEST_RUNTIME_PROFILE_ID required", file=sys.stderr)
        return 2

    for url in (ASSISTED_JUICE, INTERACTIVE_JUICE):
        urllib.request.urlopen(url, timeout=5)
        urllib.request.urlopen(url + "/api/Challenges", timeout=10)
    health = request("GET", "/health")
    log(f"daemon ok sandbox={health.get('runner', {}).get('sandbox_image')}")
    log(f"profile={PROFILE_ID} runner={RUNNER}")
    log(f"assisted_juice={ASSISTED_JUICE} interactive_juice={INTERACTIVE_JUICE}")

    # Strict isolation: separate projects + separate instances, sequential so
    # sandboxes don't fight and attribution stays obvious.
    assisted = run_mode("assisted", ASSISTED_JUICE)
    interactive = run_mode("interactive", INTERACTIVE_JUICE)

    a_n = assisted["newly_solved_count"]
    i_n = interactive["newly_solved_count"]
    if a_n > i_n:
        winner = "assisted"
    elif i_n > a_n:
        winner = "interactive"
    else:
        winner = "tie"

    report = {
        "schema": "cyberpenda-challenge-count-compare/v1",
        "method": {
            "metric": "Juice Shop GET /api/Challenges solved==true delta per isolated instance",
            "isolation": "separate docker containers + separate CyberPenda projects",
            "profile_id": PROFILE_ID,
            "runner": RUNNER,
            "model": "glm-5.2 (profile Claude Code · GLM)",
        },
        "assisted": assisted,
        "interactive": interactive,
        "verdict": {
            "winner_by_newly_solved": winner,
            "assisted_newly_solved": a_n,
            "interactive_newly_solved": i_n,
            "assisted_names": assisted["newly_solved"],
            "interactive_names": interactive["newly_solved"],
            "assisted_score_board_challenge": assisted["score_board_challenge_solved"],
            "interactive_score_board_challenge": interactive["score_board_challenge_solved"],
        },
        "generated_at": _now(),
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    out = os.environ.get(
        "PENTEST_COMPARE_REPORT_PATH",
        "scripts/challenge-count-assisted-vs-interactive-report.json",
    )
    with open(out, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2, sort_keys=True)
    log(f"report written {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
