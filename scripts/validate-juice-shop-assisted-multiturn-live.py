#!/usr/bin/env python3
"""Multi work-turn assisted Blackboard live evaluation against OWASP Juice Shop.

Advances multi work turns via the operator path that works for Claude Code when
the session is idle after conclusion:

  settle → POST /stop → POST /steer/queue → POST /resume

Native idle /steer (interrupt_then_replace) currently fails Claude with
"active turn identity mismatch", so this evaluation uses stop/queue/resume
instead of in-session steer for turns 2+.

Collects per-turn conclusion coverage, applied_revision growth, latency
distribution, and action_required / repair / retry signals. Always stops the
Task at the end to release the sandbox.
"""

from __future__ import annotations

from datetime import datetime, timezone
import json
import os
import sys
import time
import traceback
import urllib.error
import urllib.request

DAEMON = os.environ.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787").rstrip("/")
JUICE_URL = os.environ.get("PENTEST_JUICE_URL", "http://127.0.0.1:3000").rstrip("/")
JUICE_HOST = JUICE_URL.replace("https://", "").replace("http://", "").split("/")[0]
TIMEOUT_SEC = int(os.environ.get("PENTEST_JUICE_TIMEOUT_SEC", "2400"))
POLL_SEC = float(os.environ.get("PENTEST_JUICE_POLL_SEC", "5"))
TARGET_WORK_TURNS = int(os.environ.get("PENTEST_ASSISTED_TARGET_WORK_TURNS", "4"))
TURN_SETTLE_SEC = int(os.environ.get("PENTEST_ASSISTED_TURN_SETTLE_SEC", "900"))
PROFILE_ID = os.environ.get(
    "PENTEST_RUNTIME_PROFILE_ID", "b4c2e5d8cfce278732905c8616743209"
).strip()
RUNNER = os.environ.get("PENTEST_RUNNER", "sandbox").strip() or "sandbox"
EXISTING_PROJECT_ID = os.environ.get("PENTEST_PROJECT_ID", "").strip()
EXISTING_TASK_ID = os.environ.get("PENTEST_TASK_ID", "").strip()

FOLLOW_UPS = [
    (
        "Phase 2 — surface map: Use non-Blackboard tools only (curl, browser, ffuf if "
        f"available). Enumerate public routes and API endpoints on {JUICE_URL}: "
        "login/register pages, /rest/*, /api/*, robots.txt, main.js references. "
        "Summarize what you found, then end this Work Runtime Turn. "
        "Do not call Blackboard/persistence tools, do not finish the Task, "
        f"do not leave scope ({JUICE_HOST} only)."
    ),
    (
        "Phase 3 — Score Board: Locate and verify the hidden Juice Shop Score Board "
        f"route on {JUICE_URL}. Confirm HTTP reachability of the score-board page. "
        "Do not call Blackboard tools. End this Work Runtime Turn when verified. "
        f"Stay in scope: {JUICE_HOST} only."
    ),
    (
        "Phase 4 — auth probe: Inspect the login and registration surfaces on "
        f"{JUICE_URL}. Try only non-destructive checks (page load, form fields, "
        "obvious client-side validation, public user/product APIs). Do not brute-force. "
        "Do not call Blackboard tools. End this Work Runtime Turn with findings summary. "
        f"Stay in scope: {JUICE_HOST} only."
    ),
    (
        "Phase 5 — client secrets: Inspect client-side JS/HTML for interesting "
        f"comments, hardcoded keys, admin hints, or challenge clues on {JUICE_URL}. "
        "Do not call Blackboard tools. End this Work Runtime Turn. "
        f"Stay in scope: {JUICE_HOST} only."
    ),
]

GOAL = f"""Authorized multi-phase manual-style pentest of OWASP Juice Shop at {JUICE_URL}
(in scope only: {JUICE_HOST}).

This Task uses Harness-assisted Blackboard conclusion mode. Your role is Work Runtime
testing only; the Harness will persist semantics after each Work Turn.

Phase 1 (this first Work Runtime Turn):
- Recon the app with non-Blackboard tools (curl, browser, etc.).
- Confirm the app is Juice Shop, note tech stack hints, main entry paths.
- End this Work Runtime Turn after a bounded recon pass. Do not try to finish every
  challenge in one turn — the operator will send later phases as new Work Turns.

Hard rules for every Work Turn:
- Do NOT call Blackboard tools, trusted pentest MCP persistence tools, fact/finding/
  evidence write interfaces, or any equivalent semantic write path.
- Do NOT finish the Task and do NOT launch/schedule Objectives.
- Stay in scope: {JUICE_HOST} only.
- Prefer small, verifiable steps over long unbounded exploration.

If a Harness Conclude Runtime Turn asks for a closed Attempt result, describe the
work just completed as a terminal Attempt (prefer outcome "inconclusive" with
produced_targets [] unless an existing Blackboard target can be referenced). Do not
perform more testing or call tools during conclusion."""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _parse_time(value: object) -> datetime | None:
    if not isinstance(value, str) or not value.strip():
        return None
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


def request(method: str, path: str, body: dict | None = None, timeout: int = 120) -> dict:
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


def log(msg: str) -> None:
    print(f"[{_now()}] {msg}", flush=True)


def receipt_analysis(items: list[dict]) -> dict:
    pending_idxs = [
        i
        for i, item in enumerate(items)
        if item.get("type") == "harness"
        and str(item.get("content", "")).startswith(
            "Blackboard conclusion pending for work Turn "
        )
    ]
    per_turn: list[dict] = []
    latencies: list[int] = []
    applied = action_required = covered = 0
    for receipt_index, start in enumerate(pending_idxs):
        end = (
            pending_idxs[receipt_index + 1]
            if receipt_index + 1 < len(pending_idxs)
            else len(items)
        )
        closing_item = None
        closing_kind = None
        closing_content = ""
        for item in items[start + 1 : end]:
            if item.get("type") != "harness":
                continue
            content = str(item.get("content", ""))
            if content.startswith("Blackboard conclusion applied at revision "):
                closing_item, closing_kind, closing_content = item, "applied", content
                break
            if content.startswith("Blackboard conclusion requires action"):
                closing_item, closing_kind, closing_content = (
                    item,
                    "action_required",
                    content,
                )
                break
        latency_ms = None
        if closing_item is not None:
            started = _parse_time(items[start].get("created_at"))
            finished = _parse_time(closing_item.get("created_at"))
            if started is not None and finished is not None:
                latency_ms = max(0, int((finished - started).total_seconds() * 1000))
                latencies.append(latency_ms)
        if closing_kind == "applied":
            applied += 1
            covered += 1
        elif closing_kind == "action_required":
            action_required += 1
            covered += 1
        per_turn.append(
            {
                "work_turn_index": receipt_index + 1,
                "pending_content": str(items[start].get("content", "")),
                "closing_kind": closing_kind or "open",
                "closing_content": closing_content,
                "latency_ms": latency_ms,
            }
        )
    completed = len(pending_idxs)
    return {
        "completed_work_turns": completed,
        "applied_conclusions": applied,
        "action_required_receipts": action_required,
        "covered_work_turns": covered,
        "open_pending": completed - covered,
        "ratio": round(covered / completed, 3) if completed else 0.0,
        "latencies_ms": latencies,
        "per_turn": per_turn,
    }


def timeline_signals(items: list[dict]) -> dict:
    repair_retry = 0
    conclude_started = 0
    steer_failed = 0
    harness_snippets: list[str] = []
    for item in items:
        content = str(item.get("content", ""))
        lower = content.lower()
        typ = item.get("type")
        if typ == "harness":
            if content == "Blackboard Conclude Turn started":
                conclude_started += 1
            if any(word in lower for word in ("repair", "retry")):
                repair_retry += 1
            if (
                "blackboard conclusion" in lower
                or "conclude turn" in lower
                or "repair" in lower
                or "retry" in lower
            ):
                harness_snippets.append(content[:240])
        if typ == "steering" and "failed" in lower:
            steer_failed += 1
    return {
        "conclude_turns_started": conclude_started,
        "repair_or_retry_events": repair_retry,
        "steer_failed_events": steer_failed,
        "harness_snippets": harness_snippets[-40:],
        "work_tool_events": sum(
            1 for i in items if i.get("type") in {"tool_use", "tool_result"}
        ),
    }


def snapshot_state(project_id: str, task_id: str) -> dict:
    task = request("GET", f"/api/projects/{project_id}/tasks/{task_id}")
    timeline = request("GET", f"/api/projects/{project_id}/tasks/{task_id}/timeline")
    items = timeline.get("items") if isinstance(timeline, dict) else []
    if not isinstance(items, list):
        items = []
    safe = [i for i in items if isinstance(i, dict)]
    coverage = receipt_analysis(safe)
    signals = timeline_signals(safe)
    conclusion = task.get("blackboard_conclusion") or {}
    activity = task.get("runtime_activity") or {}
    return {
        "task": task,
        "status": task.get("status"),
        "conclusion_mode": conclusion.get("mode"),
        "conclusion_state": conclusion.get("state"),
        "applied_revision": conclusion.get("applied_revision"),
        "error_code": conclusion.get("error_code"),
        "liveness": activity.get("liveness"),
        "turn_activity": activity.get("turn_activity"),
        "coverage": coverage,
        "signals": signals,
        "timeline_item_count": len(safe),
    }


def wait_until_settled(
    project_id: str,
    task_id: str,
    *,
    min_completed_work_turns: int,
    deadline: float,
) -> dict:
    last: dict = {}
    turn_deadline = min(deadline, time.time() + TURN_SETTLE_SEC)
    while time.time() < turn_deadline and time.time() < deadline:
        last = snapshot_state(project_id, task_id)
        cov = last["coverage"]
        status = last["status"]
        idle_or_stopped = (
            (status == "running" and last["liveness"] == "live" and last["turn_activity"] == "idle")
            or status in {"stopped", "completed", "failed"}
        )
        settled = (
            cov["completed_work_turns"] >= min_completed_work_turns
            and cov["covered_work_turns"] >= min_completed_work_turns
            and last["conclusion_state"] in {"clean", "action_required"}
            and idle_or_stopped
        )
        log(
            f"poll turns={cov['completed_work_turns']} covered={cov['covered_work_turns']} "
            f"open={cov['open_pending']} state={last['conclusion_state']} "
            f"rev={last['applied_revision']} live={last['liveness']}/{last['turn_activity']} "
            f"status={status}"
        )
        if status == "failed" and cov["completed_work_turns"] < min_completed_work_turns:
            return last
        if (
            last["conclusion_state"] == "action_required"
            and cov["completed_work_turns"] >= min_completed_work_turns
        ):
            return last
        if settled:
            return last
        time.sleep(POLL_SEC)
    return last


def stop_task(project_id: str, task_id: str) -> dict | None:
    try:
        return request("POST", f"/api/projects/{project_id}/tasks/{task_id}/stop", {})
    except Exception as exc:  # noqa: BLE001
        log(f"stop failed: {exc}")
        return None


def wait_offline(project_id: str, task_id: str, timeout: float = 120) -> dict:
    deadline = time.time() + timeout
    last = snapshot_state(project_id, task_id)
    while time.time() < deadline:
        last = snapshot_state(project_id, task_id)
        if last["status"] != "running" and last["liveness"] != "live":
            return last
        time.sleep(2)
    return last


def advance_next_work_turn(
    project_id: str, task_id: str, message: str, phase: int
) -> dict:
    state = snapshot_state(project_id, task_id)
    if state["status"] == "running":
        log(f"phase {phase}: stop live session before queue/resume")
        stop_task(project_id, task_id)
        state = wait_offline(project_id, task_id)
        log(
            f"phase {phase}: after stop status={state['status']} "
            f"liveness={state['liveness']}"
        )

    log(f"phase {phase}: queue directive")
    queued = request(
        "POST",
        f"/api/projects/{project_id}/tasks/{task_id}/steer/queue",
        {"directive": message},
    )
    log(f"phase {phase}: resume")
    resumed = request("POST", f"/api/projects/{project_id}/tasks/{task_id}/resume", {})
    return {
        "phase": phase,
        "queued_event_id": (queued.get("event") or {}).get("id"),
        "resumed_status": resumed.get("status"),
    }


def latency_stats(latencies: list[int]) -> dict:
    if not latencies:
        return {
            "count": 0,
            "min_ms": None,
            "max_ms": None,
            "avg_ms": None,
            "p50_ms": None,
        }
    ordered = sorted(latencies)
    n = len(ordered)
    p50 = ordered[n // 2] if n % 2 else int((ordered[n // 2 - 1] + ordered[n // 2]) / 2)
    return {
        "count": n,
        "min_ms": ordered[0],
        "max_ms": ordered[-1],
        "avg_ms": int(sum(ordered) / n),
        "p50_ms": p50,
        "all_ms": ordered,
    }


def main() -> int:
    if not PROFILE_ID and not (EXISTING_PROJECT_ID and EXISTING_TASK_ID):
        print("PENTEST_RUNTIME_PROFILE_ID is required", file=sys.stderr)
        return 2

    run_started = time.time()
    deadline = run_started + TIMEOUT_SEC
    project_id = EXISTING_PROJECT_ID
    task_id = EXISTING_TASK_ID
    initial_revision = int(os.environ.get("PENTEST_INITIAL_REVISION", "0") or "0")
    revision_trace: list[dict] = []
    advance_log: list[dict] = []
    notes = [
        "Multi-turn advance uses stop→queue→resume because Claude Code idle "
        "native /steer (interrupt_then_replace) fails with "
        "'Claude active turn identity mismatch'."
    ]

    try:
        urllib.request.urlopen(JUICE_URL, timeout=5)
        health = request("GET", "/health")
        log(f"daemon ok sandbox={health.get('runner', {}).get('sandbox_image')}")
        log(f"juice shop ok {JUICE_URL}")

        if project_id and task_id:
            log(f"continuing existing project={project_id} task={task_id}")
            if "PENTEST_INITIAL_REVISION" not in os.environ:
                snap = request(
                    "GET", f"/api/v2/projects/{project_id}/blackboard/snapshot"
                )
                # When continuing, revision may already be advanced; keep env 0
                # if caller wants absolute baseline.
                initial_revision = 0
                log(f"current snapshot revision={snap.get('revision')}")
        else:
            project = request(
                "POST",
                "/api/projects",
                {
                    "name": (
                        "Juice Shop Assisted Multi-Turn Eval "
                        f"{datetime.now().strftime('%H%M%S')}"
                    ),
                    "scope": {
                        "urls": [JUICE_URL],
                        "domains": [JUICE_HOST.split(":")[0]],
                        "notes": "Multi work-turn assisted Blackboard evaluation (live billed)",
                    },
                },
            )
            project_id = project["id"]
            snapshot = request(
                "GET", f"/api/v2/projects/{project_id}/blackboard/snapshot"
            )
            initial_revision = int(snapshot.get("revision", 0))
            log(f"project={project_id} initial_revision={initial_revision}")

            run_controls: dict = {"blackboard_conclusion_mode": "assisted"}
            if RUNNER == "host":
                run_controls["host_activated"] = True
            task = request(
                "POST",
                f"/api/projects/{project_id}/tasks",
                {
                    "goal": GOAL,
                    "runtime_profile_id": PROFILE_ID,
                    "runner": RUNNER,
                    "run_controls": run_controls,
                },
            )
            task_id = task["id"]
            log(
                f"task={task_id} profile={PROFILE_ID} runner={RUNNER} assisted launched"
            )

        target = max(1, TARGET_WORK_TURNS)

        while time.time() < deadline:
            before = snapshot_state(project_id, task_id)
            covered = before["coverage"]["covered_work_turns"]
            completed = before["coverage"]["completed_work_turns"]
            log(
                f"loop head covered={covered} completed={completed} "
                f"status={before['status']} state={before['conclusion_state']}"
            )

            # If a work turn is in flight or not yet covered, wait for settlement.
            need = max(completed, covered)
            if before["status"] == "running":
                if before["coverage"]["open_pending"] > 0:
                    need = completed  # wait for open pending to close
                elif before["turn_activity"] == "busy":
                    need = completed + 1
                elif completed == 0:
                    need = 1
                else:
                    need = completed  # already settled while running
                state = wait_until_settled(
                    project_id,
                    task_id,
                    min_completed_work_turns=max(1, need),
                    deadline=deadline,
                )
            elif completed == 0 and before["status"] not in {"stopped", "failed"}:
                state = wait_until_settled(
                    project_id,
                    task_id,
                    min_completed_work_turns=1,
                    deadline=deadline,
                )
            else:
                state = before

            cov = state["coverage"]
            revision_trace.append(
                {
                    "at": _now(),
                    "completed_work_turns": cov["completed_work_turns"],
                    "applied_revision": state["applied_revision"],
                    "conclusion_state": state["conclusion_state"],
                    "latencies_ms": list(cov["latencies_ms"]),
                    "status": state["status"],
                }
            )
            log(
                f"settled checkpoint: turns={cov['completed_work_turns']} "
                f"ratio={cov['ratio']} rev={state['applied_revision']} "
                f"state={state['conclusion_state']} status={state['status']}"
            )

            if state["conclusion_state"] == "action_required":
                log(
                    f"action_required error_code={state.get('error_code')} — stop advancing"
                )
                break
            if cov["completed_work_turns"] >= target:
                log(f"reached target work turns={target}")
                break
            if state["status"] == "failed":
                log("task failed")
                break

            # Turn 1 uses the launch goal. After N completed work turns, the next
            # operator phase is FOLLOW_UPS[N-1] (N=1 → phase 2 / index 0).
            next_idx = cov["completed_work_turns"] - 1
            if next_idx < 0:
                next_idx = 0
            if next_idx >= len(FOLLOW_UPS):
                log("no more follow-up phases")
                break

            msg = FOLLOW_UPS[next_idx]
            phase = next_idx + 2
            try:
                adv = advance_next_work_turn(project_id, task_id, msg, phase)
                advance_log.append({"at": _now(), **adv})
            except Exception as exc:  # noqa: BLE001
                log(f"advance failed: {exc}")
                advance_log.append(
                    {"at": _now(), "phase": phase, "error": str(exc)[:400]}
                )
                break

            # Wait for the new turn to appear and settle.
            state = wait_until_settled(
                project_id,
                task_id,
                min_completed_work_turns=cov["completed_work_turns"] + 1,
                deadline=deadline,
            )
            # Loop continues; head will re-check counts.

        final = snapshot_state(project_id, task_id)
        final_cov = final["coverage"]
        lat = latency_stats(final_cov["latencies_ms"])
        wall_sec = round(time.time() - run_started, 1)

        log("final stop to release sandbox...")
        stop_task(project_id, task_id)
        time.sleep(2)
        after_stop = snapshot_state(project_id, task_id)

        report = {
            "schema": "cyberpenda-assisted-multiturn-live-eval/v1",
            "compared_to": {
                "narrow_script": "scripts/validate-juice-shop-assisted-live.py",
                "narrow_result_summary": {
                    "passed": True,
                    "checks": "11/11",
                    "coverage_ratio": 1.0,
                    "work_turns": 1,
                    "conclusion_latency_ms_approx": 9100,
                    "goal": "Score Board route only",
                },
            },
            "config": {
                "daemon": DAEMON,
                "juice_url": JUICE_URL,
                "profile_id": PROFILE_ID or "from-existing-task",
                "runner": RUNNER,
                "target_work_turns": target,
                "timeout_sec": TIMEOUT_SEC,
                "turn_settle_sec": TURN_SETTLE_SEC,
                "advance_strategy": "stop_queue_resume",
            },
            "ids": {
                "project_id": project_id,
                "task_id": task_id,
                "initial_revision": initial_revision,
            },
            "notes": notes,
            "final": {
                "status": after_stop["status"],
                "conclusion_mode": final["conclusion_mode"],
                "conclusion_state": final["conclusion_state"],
                "applied_revision": final["applied_revision"],
                "error_code": final.get("error_code"),
                "liveness": after_stop["liveness"],
                "turn_activity": after_stop["turn_activity"],
                "wall_clock_sec": wall_sec,
            },
            "coverage": final_cov,
            "latency": lat,
            "signals": final["signals"],
            "revision_trace": revision_trace,
            "advance_log": advance_log,
            "evaluation": {
                "multi_work_turn_achieved": final_cov["completed_work_turns"] >= 2,
                "coverage_complete": final_cov["completed_work_turns"] > 0
                and final_cov["covered_work_turns"] == final_cov["completed_work_turns"],
                "applied_revision_advanced": isinstance(final["applied_revision"], int)
                and final["applied_revision"] > initial_revision,
                "applied_revision_delta": (
                    final["applied_revision"] - initial_revision
                    if isinstance(final["applied_revision"], int)
                    else None
                ),
                "action_required_seen": final_cov["action_required_receipts"] > 0
                or final["conclusion_state"] == "action_required",
                "repair_or_retry_seen": final["signals"]["repair_or_retry_events"] > 0,
                "stop_attempted": True,
            },
        }

        print(json.dumps(report, indent=2, sort_keys=True))
        out_path = os.environ.get(
            "PENTEST_ASSISTED_REPORT_PATH",
            "scripts/assisted-multiturn-eval-report.json",
        )
        with open(out_path, "w", encoding="utf-8") as fh:
            json.dump(report, fh, indent=2, sort_keys=True)
        log(f"report written to {out_path}")
        return 0 if final_cov["covered_work_turns"] >= 1 else 1
    except Exception:
        log("fatal error:\n" + traceback.format_exc())
        if project_id and task_id:
            log("emergency stop...")
            stop_task(project_id, task_id)
        return 2


if __name__ == "__main__":
    sys.exit(main())
