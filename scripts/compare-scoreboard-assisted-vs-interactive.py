#!/usr/bin/env python3
"""One-off Score Board comparison: assisted vs interactive (same profile/model).

Launches two separate Projects against local Juice Shop, same Runtime Profile,
same Score Board goal intent, different blackboard_conclusion_mode.

Reports wall clock, tool activity, conclusion/fact success, and a simple
"did more" score for Score Board progress.
"""

from __future__ import annotations

from datetime import datetime, timezone
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
JUICE_URL = os.environ.get("PENTEST_JUICE_URL", "http://127.0.0.1:3000").rstrip("/")
JUICE_HOST = JUICE_URL.replace("https://", "").replace("http://", "").split("/")[0]
PROFILE_ID = os.environ.get(
    "PENTEST_RUNTIME_PROFILE_ID", "b4c2e5d8cfce278732905c8616743209"
).strip()
RUNNER = os.environ.get("PENTEST_RUNNER", "sandbox").strip() or "sandbox"
TIMEOUT_SEC = int(os.environ.get("PENTEST_JUICE_TIMEOUT_SEC", "1200"))
POLL_SEC = float(os.environ.get("PENTEST_JUICE_POLL_SEC", "5"))
ATTEMPT_KEY = "attempt:juice-shop-assisted-score-board"

ASSISTED_GOAL = f"""Authorized manual-style pentest of OWASP Juice Shop at {JUICE_URL} (in scope only).

Use only non-Blackboard testing tools such as browser or curl. Locate and verify
the hidden Score Board route, then end this Work Runtime Turn.

Do not call Blackboard tools, trusted pentest MCP persistence tools, fact/finding/
evidence persistence tools, or any equivalent write interface. Do not finish the
Task and do not launch or schedule another Objective. Semantic persistence is the
Harness's responsibility in assisted conclusion mode.

If the Harness later starts a Conclude Runtime Turn, describe exactly this work as
a terminal Attempt using key {ATTEMPT_KEY}. Prefer outcome "inconclusive" with
produced_targets [] unless an already-existing Blackboard target can be referenced.
Do not perform more testing or call tools during conclusion.
Stay in scope: {JUICE_HOST} only."""

INTERACTIVE_GOAL = f"""Authorized pentest of OWASP Juice Shop at {JUICE_URL} (in scope only).

Objectives:
1. Recon the web app (agent-browser or curl). Map login, API, and client-side assets.
2. Find the hidden **Score Board** challenge (Juice Shop score board / tutorial hub).
3. Record recon facts and at least one finding via trusted pentest MCP tools:
   upsert_project_fact, upsert_finding, attach_evidence when you have proof.
4. Prefer sandbox tooling (nuclei, ffuf, agent-browser). Stay in scope: {JUICE_HOST} only.
5. When Score Board is located, upsert a fact key juice-shop:score-board with summary describing how to access it.

Do not ask questions. Execute and use MCP tools for all blackboard writes."""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def log(msg: str) -> None:
    print(f"[{_now()}] {msg}", flush=True)


def request(method: str, path: str, body: dict | None = None, timeout: int = 60) -> dict:
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
        raise RuntimeError(f"{method} {path} returned HTTP {error.code}: {detail}") from error


def stop_task(project_id: str, task_id: str) -> None:
    try:
        request("POST", f"/api/projects/{project_id}/tasks/{task_id}/stop", {})
    except Exception as exc:  # noqa: BLE001
        log(f"stop {task_id}: {exc}")


def timeline_stats(items: list[dict]) -> dict:
    tool_use = sum(1 for i in items if i.get("type") == "tool_use")
    tool_result = sum(1 for i in items if i.get("type") == "tool_result")
    texts = [str(i.get("content", "")) for i in items if i.get("type") == "text"]
    joined = "\n".join(texts).lower()
    score_board_mentioned = any(
        s in joined
        for s in (
            "score-board",
            "score board",
            "scoreboard",
            "/#/score-board",
            "score_board",
        )
    )
    harness = [str(i.get("content", "")) for i in items if i.get("type") == "harness"]
    applied = sum(1 for h in harness if h.startswith("Blackboard conclusion applied"))
    pending = sum(1 for h in harness if h.startswith("Blackboard conclusion pending"))
    action_required = sum(1 for h in harness if "requires action" in h.lower())
    return {
        "tool_use": tool_use,
        "tool_result": tool_result,
        "tool_events": tool_use + tool_result,
        "text_items": len(texts),
        "score_board_mentioned_in_text": score_board_mentioned,
        "harness_pending": pending,
        "harness_applied": applied,
        "harness_action_required": action_required,
    }


def score_board_fact_hit(project_id: str) -> dict | None:
    try:
        facts = request("GET", f"/api/projects/{project_id}/facts/index").get("facts", [])
    except Exception:
        facts = []
    if not isinstance(facts, list):
        facts = []
    for fact in facts:
        if not isinstance(fact, dict):
            continue
        key = (fact.get("fact_key") or fact.get("key") or "").lower()
        summary = str(fact.get("summary") or fact.get("content") or "").lower()
        if "score-board" in key or "score board" in key or "scoreboard" in key:
            return fact
        if "score-board" in summary or "score board" in summary:
            return fact
    # Blackboard v2 records
    try:
        snap = request("GET", f"/api/v2/projects/{project_id}/blackboard/snapshot")
    except Exception:
        snap = {}
    for rec in snap.get("records") or snap.get("items") or []:
        if not isinstance(rec, dict):
            continue
        blob = json.dumps(rec, ensure_ascii=False).lower()
        if "score-board" in blob or "score board" in blob or "scoreboard" in blob:
            return {"source": "blackboard_v2_snapshot", "key": rec.get("key")}
    return None


def attempt_hit(project_id: str) -> dict | None:
    key = urllib.parse.quote(ATTEMPT_KEY, safe="")
    try:
        history = request(
            "GET", f"/api/v2/projects/{project_id}/blackboard/records/{key}/history"
        )
    except Exception:
        return None
    items = history.get("items") if isinstance(history, dict) else None
    if not isinstance(items, list):
        return None
    for item in reversed(items):
        if isinstance(item, dict) and item.get("kind") == "record":
            return item
    return None


def run_mode(mode: str) -> dict:
    if mode == "assisted":
        goal = ASSISTED_GOAL
        run_controls = {"blackboard_conclusion_mode": "assisted"}
        name = "Compare Score Board Assisted"
    else:
        goal = INTERACTIVE_GOAL
        run_controls = {"yolo": True}
        # interactive is default; omit assisted mode
        name = "Compare Score Board Interactive"

    started = time.time()
    project = request(
        "POST",
        "/api/projects",
        {
            "name": f"{name} {datetime.now().strftime('%H%M%S')}",
            "scope": {
                "urls": [JUICE_URL],
                "domains": [JUICE_HOST.split(":")[0]],
                "notes": f"one-off score board compare ({mode})",
            },
        },
    )
    project_id = project["id"]
    snap0 = request("GET", f"/api/v2/projects/{project_id}/blackboard/snapshot")
    initial_revision = int(snap0.get("revision", 0))
    task = request(
        "POST",
        f"/api/projects/{project_id}/tasks",
        {
            "goal": goal,
            "runtime_profile_id": PROFILE_ID,
            "runner": RUNNER,
            "run_controls": run_controls,
        },
    )
    task_id = task["id"]
    log(f"{mode}: project={project_id} task={task_id}")

    success = False
    reason = "timeout"
    last_task: dict = {}
    last_stats: dict = {}
    deadline = time.time() + TIMEOUT_SEC
    while time.time() < deadline:
        last_task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
        timeline = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/timeline")
        items = [i for i in (timeline.get("items") or []) if isinstance(i, dict)]
        last_stats = timeline_stats(items)
        status = last_task.get("status")
        activity = last_task.get("runtime_activity") or {}
        conclusion = last_task.get("blackboard_conclusion") or {}

        fact = score_board_fact_hit(project_id)
        attempt = attempt_hit(project_id) if mode == "assisted" else None

        if mode == "assisted":
            applied = (
                conclusion.get("state") == "clean"
                and isinstance(conclusion.get("applied_revision"), int)
                and conclusion.get("applied_revision") > initial_revision
            )
            if (
                applied
                and activity.get("liveness") == "live"
                and activity.get("turn_activity") == "idle"
            ):
                success = True
                reason = (
                    "assisted conclusion applied"
                    + ("; score board mentioned" if last_stats["score_board_mentioned_in_text"] else "")
                    + ("; attempt present" if attempt else "")
                    + ("; fact present" if fact else "")
                )
                break
            if conclusion.get("state") == "action_required" or status not in {
                "running",
                "starting",
            }:
                reason = f"stopped early status={status} conclusion={conclusion.get('state')}"
                break
        else:
            if fact:
                success = True
                reason = f"score-board fact: {fact.get('fact_key') or fact.get('key') or fact}"
                break
            if last_stats["score_board_mentioned_in_text"] and status in {
                "completed",
                "stopped",
                "failed",
            }:
                # Weak success: mentioned but no fact
                success = False
                reason = f"task {status}; score board mentioned without fact"
                break
            if status in {"completed", "failed", "stopped"}:
                findings = request("GET", f"/api/projects/{project_id}/findings").get(
                    "findings", []
                )
                reason = f"task {status}; facts/findings without score-board fact; findings={len(findings) if isinstance(findings, list) else 0}"
                break

        log(
            f"{mode}: status={status} live={activity.get('liveness')}/{activity.get('turn_activity')} "
            f"tools={last_stats['tool_events']} sb_text={last_stats['score_board_mentioned_in_text']} "
            f"conclusion={conclusion.get('state')} rev={conclusion.get('applied_revision')}"
        )
        time.sleep(POLL_SEC)

    wall = round(time.time() - started, 1)
    fact = score_board_fact_hit(project_id)
    attempt = attempt_hit(project_id) if mode == "assisted" else None
    conclusion = last_task.get("blackboard_conclusion") or {}
    activity = last_task.get("runtime_activity") or {}
    snap1 = request("GET", f"/api/v2/projects/{project_id}/blackboard/snapshot")
    final_revision = int(snap1.get("revision", 0))

    # Always stop to free sandbox
    stop_task(project_id, task_id)
    time.sleep(1)
    after = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")

    # "How much Score Board work" heuristic score
    score = 0
    if last_stats.get("score_board_mentioned_in_text"):
        score += 3
    if fact:
        score += 5
    if attempt:
        score += 2
    if mode == "assisted" and isinstance(conclusion.get("applied_revision"), int) and conclusion.get("applied_revision") > initial_revision:
        score += 2
    score += min(last_stats.get("tool_events", 0), 20) // 2  # up to +10 for activity

    return {
        "mode": mode,
        "project_id": project_id,
        "task_id": task_id,
        "success": success,
        "reason": reason,
        "wall_clock_sec": wall,
        "status_final": after.get("status"),
        "liveness_final": (after.get("runtime_activity") or {}).get("liveness"),
        "initial_revision": initial_revision,
        "final_revision": final_revision,
        "applied_revision": conclusion.get("applied_revision"),
        "conclusion_state": conclusion.get("state"),
        "timeline": last_stats,
        "score_board_fact": bool(fact),
        "score_board_attempt": bool(attempt),
        "progress_score": score,
        "activity_at_success": activity,
    }


def main() -> int:
    if not PROFILE_ID:
        print("PENTEST_RUNTIME_PROFILE_ID required", file=sys.stderr)
        return 2
    urllib.request.urlopen(JUICE_URL, timeout=5)
    health = request("GET", "/health")
    log(f"daemon ok sandbox={health.get('runner', {}).get('sandbox_image')}")
    log(f"profile={PROFILE_ID} runner={RUNNER}")

    results = []
    for mode in ("assisted", "interactive"):
        log(f"=== starting {mode} ===")
        try:
            results.append(run_mode(mode))
        except Exception as exc:  # noqa: BLE001
            log(f"{mode} fatal: {exc}")
            results.append({"mode": mode, "success": False, "reason": str(exc)[:500], "progress_score": 0})
        log(f"=== finished {mode}: {results[-1].get('reason')} score={results[-1].get('progress_score')} ===")
        time.sleep(3)

    assisted = next((r for r in results if r.get("mode") == "assisted"), {})
    interactive = next((r for r in results if r.get("mode") == "interactive"), {})
    a_score = assisted.get("progress_score") or 0
    i_score = interactive.get("progress_score") or 0
    if a_score > i_score:
        winner = "assisted"
    elif i_score > a_score:
        winner = "interactive"
    else:
        winner = "tie"

    report = {
        "schema": "cyberpenda-scoreboard-mode-compare/v1",
        "profile_id": PROFILE_ID,
        "runner": RUNNER,
        "juice_url": JUICE_URL,
        "results": results,
        "verdict": {
            "winner_by_progress_score": winner,
            "assisted_progress_score": a_score,
            "interactive_progress_score": i_score,
            "assisted_success": assisted.get("success"),
            "interactive_success": interactive.get("success"),
            "notes": [
                "progress_score weights: fact(+5), text mention(+3), attempt(+2), applied revision(+2), tool activity(up to +10).",
                "assisted success = conclusion applied + live idle; interactive success = score-board fact present.",
            ],
        },
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    out = os.environ.get(
        "PENTEST_COMPARE_REPORT_PATH",
        "scripts/scoreboard-assisted-vs-interactive-report.json",
    )
    with open(out, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2, sort_keys=True)
    log(f"report written {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
