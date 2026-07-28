#!/usr/bin/env python3
"""Run and validate a Harness-assisted Juice Shop Task.

Unlike run-juice-shop-live.py, the Work Runtime is explicitly forbidden from
writing Blackboard. The validator observes only operator APIs and emits an
allowlisted JSON acceptance report.
"""

from __future__ import annotations

from datetime import datetime
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
JUICE_URL = os.environ.get("PENTEST_JUICE_URL", "http://127.0.0.1:3000").rstrip("/")
JUICE_HOST = JUICE_URL.replace("https://", "").replace("http://", "").split("/")[0]
TIMEOUT_SEC = int(os.environ.get("PENTEST_JUICE_TIMEOUT_SEC", "1800"))
POLL_SEC = float(os.environ.get("PENTEST_JUICE_POLL_SEC", "3"))
ATTEMPT_KEY = "attempt:juice-shop-assisted-score-board"
TERMINAL_ATTEMPT_STATUSES = {"succeeded", "failed", "blocked", "inconclusive"}

GOAL = f"""Authorized manual-style pentest of OWASP Juice Shop at {JUICE_URL} (in scope only).

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


def build_launch_payload(runtime_profile_id: str, runner: str) -> dict:
    run_controls = {"blackboard_conclusion_mode": "assisted"}
    if runner == "host":
        run_controls["host_activated"] = True
    return {
        "goal": GOAL,
        "runtime_profile_id": runtime_profile_id,
        "runner": runner,
        "run_controls": run_controls,
    }


def _parse_time(value: object) -> datetime | None:
    if not isinstance(value, str) or not value.strip():
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


def _receipt_coverage(items: list[dict]) -> tuple[dict, list[int]]:
    pending = [
        index
        for index, item in enumerate(items)
        if item.get("type") == "harness"
        and str(item.get("content", "")).startswith("Blackboard conclusion pending for work Turn ")
    ]
    applied = 0
    action_required = 0
    covered = 0
    latencies: list[int] = []
    for receipt_index, start in enumerate(pending):
        end = pending[receipt_index + 1] if receipt_index + 1 < len(pending) else len(items)
        closing_item = None
        closing_kind = None
        for item in items[start + 1 : end]:
            content = str(item.get("content", ""))
            if item.get("type") != "harness":
                continue
            if content.startswith("Blackboard conclusion applied at revision "):
                closing_item, closing_kind = item, "applied"
                break
            if content.startswith("Blackboard conclusion requires action"):
                closing_item, closing_kind = item, "action_required"
                break
        if closing_kind == "applied":
            applied += 1
            covered += 1
        elif closing_kind == "action_required":
            action_required += 1
            covered += 1
        if closing_item is not None:
            started = _parse_time(items[start].get("created_at"))
            finished = _parse_time(closing_item.get("created_at"))
            if started is not None and finished is not None:
                latencies.append(max(0, int((finished - started).total_seconds() * 1000)))
    completed = len(pending)
    return {
        "completed_work_turns": completed,
        "applied_conclusions": applied,
        "action_required_receipts": action_required,
        "covered_work_turns": covered,
        "ratio": round(covered / completed, 3) if completed else 0.0,
    }, latencies


def _work_debt_events(events: dict | None) -> int:
    records = events.get("events") if isinstance(events, dict) else []
    if not isinstance(records, list):
        return 0
    count = 0
    for event in records:
        if not isinstance(event, dict) or event.get("kind") != "blackboard_conclusion":
            continue
        payload = event.get("payload")
        if not isinstance(payload, dict) or payload.get("phase") != "pending_detected":
            continue
        source = payload.get("source_work_watermark")
        persisted = payload.get("semantic_persistence_watermark")
        if isinstance(source, int) and isinstance(persisted, int) and source > persisted:
            count += 1
    return count


def evaluate_observation(
    *,
    initial_revision: int,
    task: dict,
    timeline: dict,
    attempt_detail: dict | None,
    events: dict | None = None,
) -> dict:
    """Evaluate API observations and return only bounded, non-sensitive evidence."""
    items = timeline.get("items") if isinstance(timeline, dict) else []
    if not isinstance(items, list):
        items = []
    safe_items = [item for item in items if isinstance(item, dict)]
    coverage, latencies = _receipt_coverage(safe_items)
    work_debt_events = _work_debt_events(events)
    conclusion = task.get("blackboard_conclusion") or {}
    activity = task.get("runtime_activity") or {}
    applied_revision = conclusion.get("applied_revision")
    attempt_status = (
        (attempt_detail.get("record") or {}).get("status")
        if isinstance(attempt_detail, dict) and attempt_detail.get("type") == "attempt"
        else None
    )
    contents = [str(item.get("content", "")).lower() for item in safe_items]
    automatic_control_turns = sum(
        item.get("type") == "harness"
        and str(item.get("content", "")) == "Blackboard Conclude Turn started"
        for item in safe_items
    )
    no_finish = not any(
        "task finish" in content or "task completed" in content for content in contents
    ) and task.get("status") == "running"
    no_objective_dispatch = not any(
        "objective dispatch" in content or "reason task dispatch" in content
        for content in contents
    )
    checks = {
        "assisted_mode": conclusion.get("mode") == "assisted",
        "conclusion_applied": conclusion.get("state") == "clean"
        and isinstance(applied_revision, int)
        and applied_revision > initial_revision,
        "terminal_attempt": attempt_status in TERMINAL_ATTEMPT_STATUSES,
        "task_running": task.get("status") == "running",
        "runtime_live_idle": activity.get("liveness") == "live"
        and activity.get("turn_activity") == "idle",
        # A pending_detected Task Event is written from normalized provider
        # observations only when non-Blackboard Tool Results advanced the Work
        # watermark beyond semantic persistence. This is portable across
        # providers and does not expose raw tool payloads.
        "work_turn_visible": work_debt_events > 0,
        "conclude_turn_visible": any("blackboard conclude turn" in content for content in contents),
        "applied_revision_visible": coverage["applied_conclusions"] > 0,
        "coverage_complete": coverage["completed_work_turns"] > 0
        and coverage["covered_work_turns"] == coverage["completed_work_turns"],
        "no_automatic_task_finish": no_finish,
        "no_automatic_objective_dispatch": no_objective_dispatch,
    }
    return {
        "schema": "cyberpenda-assisted-live-validation/v1",
        "passed": all(checks.values()),
        "checks": checks,
        "task": {
            "status": task.get("status"),
            "runtime_liveness": activity.get("liveness"),
            "runtime_turn_activity": activity.get("turn_activity"),
            "conclusion_state": conclusion.get("state"),
            "initial_revision": initial_revision,
            "applied_revision": applied_revision,
            "attempt_status": attempt_status,
        },
        "coverage": coverage,
        "harness": {
            "conclusion_latency_ms": latencies,
            "model_usage": {
                "unit": "runtime_turns",
                "work_turns": coverage["completed_work_turns"],
                "automatic_control_turns": automatic_control_turns,
                "provider_token_usage_available": False,
            },
        },
        "timeline": {
            "work_debt_events": work_debt_events,
            "work_events": sum(item.get("type") in {"tool_use", "tool_result"} for item in safe_items),
            "conclude_events": sum(
                item.get("type") == "harness" and "blackboard conclude turn" in str(item.get("content", "")).lower()
                for item in safe_items
            ),
            "repair_or_retry_events": sum(
                item.get("type") == "harness"
                and any(word in str(item.get("content", "")).lower() for word in ("repair", "retry"))
                for item in safe_items
            ),
        },
    }


def request(method: str, path: str, body: dict | None = None) -> dict:
    data = None if body is None else json.dumps(body).encode()
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("PENTEST_AUTH_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(DAEMON + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            raw = response.read().decode()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as error:
        detail = error.read().decode()[:500]
        raise RuntimeError(f"{method} {path} returned HTTP {error.code}: {detail}") from error


def ensure_juice_shop() -> None:
    try:
        urllib.request.urlopen(JUICE_URL, timeout=5)
        return
    except Exception:
        pass
    subprocess.run(
        ["docker", "run", "-d", "--rm", "--name", "pentest-juice-shop", "-p", "3000:3000", "bkimminich/juice-shop"],
        check=True,
    )
    deadline = time.time() + 120
    while time.time() < deadline:
        try:
            urllib.request.urlopen(JUICE_URL, timeout=3)
            return
        except Exception:
            time.sleep(2)
    raise RuntimeError(f"Juice Shop not ready at {JUICE_URL}")


def _attempt_detail(project_id: str) -> dict | None:
    """Load the terminal Attempt from history.

    Blackboard v2 removes terminal Attempts from Current Truth while retaining
    them in semantic history, so the operator record GET is expected to 404
    after a successful assisted conclusion.
    """
    key = urllib.parse.quote(ATTEMPT_KEY, safe="")
    try:
        history = request("GET", f"/api/v2/projects/{project_id}/blackboard/records/{key}/history")
    except RuntimeError as error:
        if "HTTP 404" in str(error):
            return None
        raise
    items = history.get("items") if isinstance(history, dict) else None
    if not isinstance(items, list):
        return None
    for item in reversed(items):
        if not isinstance(item, dict) or item.get("kind") != "record":
            continue
        if item.get("type") != "attempt" and not str(item.get("key", "")).startswith("attempt:"):
            continue
        record = item.get("record") if isinstance(item.get("record"), dict) else {}
        status = record.get("status")
        if status in TERMINAL_ATTEMPT_STATUSES:
            return {
                "type": "attempt",
                "key": item.get("key") or ATTEMPT_KEY,
                "version": item.get("version"),
                "record": record,
                "source": "history",
            }
    return None


def wait_for_observation(project_id: str, task_id: str, initial_revision: int) -> dict:
    deadline = time.time() + TIMEOUT_SEC
    last_evidence: dict = {}
    while time.time() < deadline:
        task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
        timeline = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/timeline")
        events = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/events")
        attempt = _attempt_detail(project_id)
        last_evidence = evaluate_observation(
            initial_revision=initial_revision,
            task=task,
            timeline=timeline,
            events=events,
            attempt_detail=attempt,
        )
        if last_evidence["passed"]:
            return last_evidence
        conclusion_state = last_evidence.get("task", {}).get("conclusion_state")
        if conclusion_state == "action_required" or task.get("status") != "running":
            return last_evidence
        time.sleep(POLL_SEC)
    return last_evidence


def main() -> int:
    profile_id = os.environ.get("PENTEST_RUNTIME_PROFILE_ID", "").strip()
    if not profile_id:
        print("PENTEST_RUNTIME_PROFILE_ID is required", file=sys.stderr)
        return 2
    runner = os.environ.get("PENTEST_RUNNER", "sandbox").strip() or "sandbox"
    ensure_juice_shop()
    project = request("POST", "/api/projects", {
        "name": "Juice Shop Assisted Blackboard Validation",
        "scope": {
            "urls": [JUICE_URL],
            "domains": [JUICE_HOST.split(":")[0]],
            "notes": "Manual-style assisted Blackboard acceptance run",
        },
    })
    project_id = project["id"]
    snapshot = request("GET", f"/api/v2/projects/{project_id}/blackboard/snapshot")
    initial_revision = int(snapshot.get("revision", 0))
    task = request(
        "POST",
        f"/api/projects/{project_id}/tasks",
        build_launch_payload(profile_id, runner),
    )
    task_id = task["id"]
    evidence = wait_for_observation(project_id, task_id, initial_revision)
    evidence["project_id"] = project_id
    evidence["task_id"] = task_id
    print(json.dumps(evidence, indent=2, sort_keys=True))
    return 0 if evidence.get("passed") else 1


if __name__ == "__main__":
    sys.exit(main())
