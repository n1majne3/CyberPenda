#!/usr/bin/env python3
"""Run CyberGym level1 30-task eval in batches of 5 until complete.

- Waits for any in-flight compare-cybergym process
- Pulls n132/arvo:<id>-vul/fix per batch (skip if present)
- Runs compare-cybergym-assisted-vs-interactive.py
- Archives each batch report under scripts/cybergym-batches/
- Skips tasks whose images fail to pull after retries
- Writes final summary scripts/cybergym-level1-30-summary.json + .md

Progress is tracked via scripts/cybergym-batches/progress.json so restarts resume.
"""

from __future__ import annotations

from datetime import datetime, timezone
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
SCRIPTS = REPO / "scripts"
BATCH_DIR = SCRIPTS / "cybergym-batches"
PROGRESS_PATH = BATCH_DIR / "progress.json"
SUMMARY_JSON = SCRIPTS / "cybergym-level1-30-summary.json"
SUMMARY_MD = SCRIPTS / "cybergym-level1-30-summary.md"
TASKS_FILE = Path(
    os.environ.get(
        "CYBERGYM_LEVEL1_30_JSON",
        "/Users/benjamin/tools/validation-benchmarks/cybergym_data/level1_30_tasks.json",
    )
)
COMPARE_SCRIPT = SCRIPTS / "compare-cybergym-assisted-vs-interactive.py"
CYBERGYM_ROOT = Path(
    os.environ.get(
        "CYBERGYM_ROOT",
        "/Users/benjamin/tools/validation-benchmarks/cybergym",
    )
)
POC_DB = CYBERGYM_ROOT / "server_poc" / "poc.db"
PROFILE_ID = os.environ.get(
    "PENTEST_RUNTIME_PROFILE_ID", "e7962d2782982265cb60da4f293aac57"
)
BATCH_SIZE = int(os.environ.get("CYBERGYM_BATCH_SIZE", "5"))
PULL_RETRIES = int(os.environ.get("CYBERGYM_PULL_RETRIES", "2"))
MIN_FREE_GB = float(os.environ.get("CYBERGYM_MIN_FREE_GB", "25"))
# Known-bad pulls can be skipped via env (comma-separated arvo ids)
SKIP_IDS = {
    x.strip()
    for x in os.environ.get("CYBERGYM_SKIP_TASK_IDS", "arvo:10400").split(",")
    if x.strip()
}


def log(msg: str) -> None:
    print(f"[{datetime.now(timezone.utc).isoformat()}] {msg}", flush=True)


def load_task_ids() -> list[str]:
    data = json.loads(TASKS_FILE.read_text())
    return list(data["task_ids"])


def load_progress() -> dict:
    if PROGRESS_PATH.exists():
        return json.loads(PROGRESS_PATH.read_text())
    return {
        "completed_tasks": [],
        "skipped_tasks": [],
        "batches": [],
        "started_at": datetime.now(timezone.utc).isoformat(),
    }


def save_progress(prog: dict) -> None:
    BATCH_DIR.mkdir(parents=True, exist_ok=True)
    PROGRESS_PATH.write_text(json.dumps(prog, indent=2) + "\n")


def free_gb() -> float:
    st = os.statvfs(str(Path.home()))
    return (st.f_bavail * st.f_frsize) / (1024**3)


def compare_running() -> bool:
    try:
        out = subprocess.check_output(["pgrep", "-f", "compare-cybergym-assisted-vs-interactive.py"], text=True)
        return bool(out.strip())
    except subprocess.CalledProcessError:
        return False


def wait_for_compare(poll: int = 30) -> None:
    if not compare_running():
        return
    log("waiting for in-flight compare to finish...")
    while compare_running():
        time.sleep(poll)
    log("in-flight compare finished")
    # Let last report flush
    time.sleep(3)


def arvo_id(task_id: str) -> str:
    return task_id.split(":", 1)[1]


def image_present(tag: str) -> bool:
    r = subprocess.run(
        ["docker", "image", "inspect", f"n132/arvo:{tag}"],
        capture_output=True,
    )
    return r.returncode == 0


def pull_image(tag: str) -> bool:
    if image_present(tag):
        log(f"  skip image {tag}")
        return True
    for attempt in range(1, PULL_RETRIES + 1):
        log(f"  pull n132/arvo:{tag} (attempt {attempt}/{PULL_RETRIES})")
        r = subprocess.run(["docker", "pull", f"n132/arvo:{tag}"], capture_output=False)
        if r.returncode == 0 and image_present(tag):
            log(f"  ok {tag}")
            return True
        log(f"  fail {tag} rc={r.returncode}")
        time.sleep(5)
    return False


def ensure_images(task_ids: list[str]) -> list[str]:
    """Return task_ids for which both vul and fix images are available."""
    ready = []
    for tid in task_ids:
        aid = arvo_id(tid)
        ok_vul = pull_image(f"{aid}-vul")
        ok_fix = pull_image(f"{aid}-fix")
        if ok_vul and ok_fix:
            ready.append(tid)
        else:
            log(f"SKIP {tid}: missing images vul={ok_vul} fix={ok_fix}")
    return ready


def ensure_cybergym_server() -> None:
    try:
        urllib.request.urlopen("http://127.0.0.1:8666/docs", timeout=5).read()
        return
    except Exception:
        pass
    log("starting cybergym server...")
    env = os.environ.copy()
    env.setdefault(
        "CYBERGYM_API_KEY", "cybergym-030a0cd7-5908-4862-8ab9-91f2bfc7b56d"
    )
    log_dir = CYBERGYM_ROOT / "server_poc"
    log_dir.mkdir(parents=True, exist_ok=True)
    subprocess.Popen(
        [
            str(CYBERGYM_ROOT / ".venv" / "bin" / "python"),
            "-m",
            "cybergym.server",
            "--host",
            "0.0.0.0",
            "--port",
            "8666",
            "--mask_map_path",
            "mask_map.json",
            "--log_dir",
            str(log_dir),
            "--db_path",
            str(log_dir / "poc.db"),
        ],
        cwd=str(CYBERGYM_ROOT),
        env=env,
        stdout=open(log_dir / "server.log", "a"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    time.sleep(3)
    urllib.request.urlopen("http://127.0.0.1:8666/docs", timeout=10).read()


def run_batch(batch_num: int, task_ids: list[str], prog: dict) -> dict:
    ensure_cybergym_server()
    free = free_gb()
    log(f"batch {batch_num}: free_disk={free:.1f}Gi tasks={task_ids}")
    if free < MIN_FREE_GB:
        log(f"WARNING low disk {free:.1f}Gi < {MIN_FREE_GB}Gi — continuing carefully")

    ready = ensure_images(task_ids)
    for tid in task_ids:
        if tid not in ready and tid not in prog["skipped_tasks"]:
            prog["skipped_tasks"].append(tid)
    save_progress(prog)
    if not ready:
        return {"batch": batch_num, "task_ids": task_ids, "ready": [], "error": "no images"}

    # Archive previous live log/report
    live_log = SCRIPTS / "cybergym-compare-run.log"
    live_report = SCRIPTS / "cybergym-assisted-vs-interactive-report.json"
    BATCH_DIR.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    if live_log.exists():
        shutil.move(str(live_log), str(BATCH_DIR / f"compare-run-before-b{batch_num}-{stamp}.log"))
    if live_report.exists():
        # keep a copy before overwrite
        shutil.copy2(live_report, BATCH_DIR / f"report-before-b{batch_num}-{stamp}.json")

    env = os.environ.copy()
    env.update(
        {
            "PENTEST_DAEMON_URL": env.get("PENTEST_DAEMON_URL", "http://127.0.0.1:8787"),
            "PENTEST_RUNTIME_PROFILE_ID": PROFILE_ID,
            "PENTEST_RUNNER": "sandbox",
            "PENTEST_CYBERGYM_TIMEOUT_SEC": env.get("PENTEST_CYBERGYM_TIMEOUT_SEC", "2400"),
            "PENTEST_IDLE_STABLE_SEC": env.get("PENTEST_IDLE_STABLE_SEC", "300"),
            "CYBERGYM_TASK_IDS": ",".join(ready),
            "CYBERGYM_MODES": "assisted,interactive",
            "CYBERGYM_DIFFICULTY": "level1",
            # Parallel (mode,task) workers — default 4 (conservative; override via env)
            "CYBERGYM_PARALLEL_WORKERS": env.get("CYBERGYM_PARALLEL_WORKERS", "4"),
            "CYBERGYM_API_KEY": env.get(
                "CYBERGYM_API_KEY", "cybergym-030a0cd7-5908-4862-8ab9-91f2bfc7b56d"
            ),
            "CYBERGYM_SERVER_HOST": "http://127.0.0.1:8666",
            "CYBERGYM_SERVER_PUBLIC": "http://host.docker.internal:8666",
            "PYTHONUNBUFFERED": "1",
        }
    )
    log_path = BATCH_DIR / f"batch{batch_num}-run.log"
    log(f"batch {batch_num}: starting compare ready={ready} log={log_path}")
    with open(log_path, "w") as lf:
        lf.write(f"batch={batch_num} tasks={ready} profile={PROFILE_ID}\n")
        lf.flush()
        # also tee to live path for monitoring
        proc = subprocess.Popen(
            [sys.executable, "-u", str(COMPARE_SCRIPT)],
            cwd=str(REPO),
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
        live_fp = open(live_log, "w")
        assert proc.stdout is not None
        for line in proc.stdout:
            live_fp.write(line)
            live_fp.flush()
            lf.write(line)
            lf.flush()
            # echo progress lines
            if any(
                k in line
                for k in (
                    "gen_task",
                    "submissions=",
                    "vul crash",
                    "settle",
                    "scoreboard",
                    "SOLVED",
                    "wrote ",
                    "start modes",
                )
            ):
                print(line.rstrip(), flush=True)
        rc = proc.wait()
        live_fp.close()
    log(f"batch {batch_num}: compare exit={rc}")

    report_src = live_report
    report_dst = BATCH_DIR / f"batch{batch_num}-report.json"
    batch_rec: dict = {
        "batch": batch_num,
        "task_ids": ready,
        "compare_rc": rc,
        "log": str(log_path),
        "finished_at": datetime.now(timezone.utc).isoformat(),
        "free_gb_after": free_gb(),
    }
    if report_src.exists():
        shutil.copy2(report_src, report_dst)
        batch_rec["report"] = str(report_dst)
        try:
            rep = json.loads(report_src.read_text())
            batch_rec["scoreboard"] = rep.get("scoreboard")
            # mark completed
            for tid in ready:
                if tid not in prog["completed_tasks"]:
                    prog["completed_tasks"].append(tid)
        except Exception as exc:  # noqa: BLE001
            batch_rec["report_error"] = str(exc)
    else:
        batch_rec["error"] = "missing report"
        # still mark attempted as completed to avoid infinite loop, but record fail
        for tid in ready:
            if tid not in prog["completed_tasks"]:
                prog["completed_tasks"].append(tid)

    prog["batches"].append(batch_rec)
    save_progress(prog)
    return batch_rec


def seed_progress_from_known_rounds(prog: dict, all_tasks: list[str]) -> dict:
    """Import round1/round2 reports if present so we don't re-run finished tasks."""
    known_reports = [
        SCRIPTS / "cybergym-report-round1-hub.json",
        SCRIPTS / "cybergym-assisted-vs-interactive-report.json",
        BATCH_DIR / "batch1-report.json",
        BATCH_DIR / "batch2-report.json",
    ]
    # Also parse round1 report path used earlier
    for path in known_reports:
        if not path.exists():
            continue
        try:
            rep = json.loads(path.read_text())
        except Exception:
            continue
        tids = rep.get("task_ids") or []
        # only accept if tasks are subset of the 30
        for tid in tids:
            if tid in all_tasks and tid not in prog["completed_tasks"]:
                # If report is mid-flight incomplete (round2 still running), only
                # trust full scoreboard rows
                prog["completed_tasks"].append(tid)
        # If this is the live report for round2 still incomplete, don't mark incomplete tasks
        # Recompute carefully from scoreboard
    # Prefer scoreboard-based completion from archived + live reports
    prog["completed_tasks"] = []
    for path in [
        SCRIPTS / "cybergym-report-round1-hub.json",
        *sorted(BATCH_DIR.glob("batch*-report.json")),
    ]:
        if not path.exists():
            continue
        try:
            rep = json.loads(path.read_text())
        except Exception:
            continue
        for tid in rep.get("task_ids") or []:
            if tid in all_tasks and tid not in prog["completed_tasks"]:
                prog["completed_tasks"].append(tid)
    # Live report: only add tasks that appear with both modes in results
    live = SCRIPTS / "cybergym-assisted-vs-interactive-report.json"
    if live.exists():
        try:
            rep = json.loads(live.read_text())
            modes_by_task: dict[str, set[str]] = {}
            for r in rep.get("results") or []:
                modes_by_task.setdefault(r.get("task_id"), set()).add(r.get("mode"))
            for tid, modes in modes_by_task.items():
                if modes >= {"assisted", "interactive"} and tid in all_tasks:
                    if tid not in prog["completed_tasks"]:
                        prog["completed_tasks"].append(tid)
        except Exception:
            pass
    save_progress(prog)
    return prog


def build_summary(prog: dict, all_tasks: list[str]) -> dict:
    """Aggregate all batch reports + round1 archive into final summary."""
    reports = []
    for path in [
        SCRIPTS / "cybergym-report-round1-hub.json",
        *sorted(BATCH_DIR.glob("batch*-report.json")),
        SCRIPTS / "cybergym-assisted-vs-interactive-report.json",
    ]:
        if path.exists():
            try:
                reports.append((str(path), json.loads(path.read_text())))
            except Exception:
                pass

    # Deduplicate results by (task_id, mode) keeping latest
    by_key: dict[tuple[str, str], dict] = {}
    profile = PROFILE_ID
    for path, rep in reports:
        profile = rep.get("profile_id") or profile
        for r in rep.get("results") or []:
            tid, mode = r.get("task_id"), r.get("mode")
            if not tid or not mode:
                continue
            by_key[(tid, mode)] = {**r, "_source": path}

    rows = []
    a_ok = i_ok = 0
    for tid in all_tasks:
        a = by_key.get((tid, "assisted"))
        i = by_key.get((tid, "interactive"))
        row = {
            "task_id": tid,
            "assisted": None if not a else {
                "solved": bool(a.get("solved")),
                "submissions": (a.get("poc") or {}).get("submissions"),
                "vul_crashes": (a.get("poc") or {}).get("vul_crashes"),
                "wall_clock_sec": a.get("wall_clock_sec"),
            },
            "interactive": None if not i else {
                "solved": bool(i.get("solved")),
                "submissions": (i.get("poc") or {}).get("submissions"),
                "vul_crashes": (i.get("poc") or {}).get("vul_crashes"),
                "wall_clock_sec": i.get("wall_clock_sec"),
            },
            "skipped": tid in prog.get("skipped_tasks", []),
        }
        if row["assisted"] and row["assisted"]["solved"]:
            a_ok += 1
        if row["interactive"] and row["interactive"]["solved"]:
            i_ok += 1
        rows.append(row)

    both = sum(
        1
        for r in rows
        if r.get("assisted")
        and r.get("interactive")
        and r["assisted"]["solved"]
        and r["interactive"]["solved"]
    )
    neither = sum(
        1
        for r in rows
        if r.get("assisted")
        and r.get("interactive")
        and not r["assisted"]["solved"]
        and not r["interactive"]["solved"]
    )
    a_only = sum(
        1
        for r in rows
        if r.get("assisted")
        and r.get("interactive")
        and r["assisted"]["solved"]
        and not r["interactive"]["solved"]
    )
    i_only = sum(
        1
        for r in rows
        if r.get("assisted")
        and r.get("interactive")
        and not r["assisted"]["solved"]
        and r["interactive"]["solved"]
    )
    evaluated = sum(1 for r in rows if r.get("assisted") and r.get("interactive"))

    summary = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "profile_id": profile,
        "difficulty": "level1",
        "total_planned": len(all_tasks),
        "evaluated_both_modes": evaluated,
        "skipped_tasks": prog.get("skipped_tasks", []),
        "completed_tasks": prog.get("completed_tasks", []),
        "totals": {
            "assisted_solved": a_ok,
            "interactive_solved": i_ok,
            "both_solved": both,
            "assisted_only": a_only,
            "interactive_only": i_only,
            "neither": neither,
            "evaluated": evaluated,
        },
        "scoreboard": rows,
        "batches": prog.get("batches", []),
    }
    return summary


def write_summary_md(summary: dict) -> None:
    t = summary["totals"]
    lines = [
        "# CyberGym level1 × 30 — final summary",
        "",
        f"- Generated: `{summary['generated_at']}`",
        f"- Profile: `{summary['profile_id']}`",
        f"- Planned: {summary['total_planned']}",
        f"- Evaluated (both modes): {t['evaluated']}",
        f"- Skipped: {', '.join(summary.get('skipped_tasks') or []) or '(none)'}",
        "",
        "## Totals",
        "",
        f"| Metric | Count |",
        f"|--------|------:|",
        f"| assisted solved | {t['assisted_solved']}/{t['evaluated']} |",
        f"| interactive solved | {t['interactive_solved']}/{t['evaluated']} |",
        f"| both solved | {t['both_solved']} |",
        f"| assisted only | {t['assisted_only']} |",
        f"| interactive only | {t['interactive_only']} |",
        f"| neither | {t['neither']} |",
        "",
        "## Scoreboard",
        "",
        "| Task | assisted | interactive |",
        "|------|----------|-------------|",
    ]
    for r in summary["scoreboard"]:
        def cell(side):
            if r.get("skipped") and not side:
                return "SKIP"
            if not side:
                return "—"
            mark = "SOLVED" if side.get("solved") else "fail"
            return f"{mark} s={side.get('submissions')} c={side.get('vul_crashes')} t={side.get('wall_clock_sec')}"

        lines.append(
            f"| {r['task_id']} | {cell(r.get('assisted'))} | {cell(r.get('interactive'))} |"
        )
    lines.append("")
    lines.append("## Notes")
    lines.append("")
    lines.append(
        "- Success = PoC crashes vul image (exit ∉ {0,300}) and fix image exits 0."
    )
    lines.append("- Modes: assisted vs interactive Blackboard conclusion.")
    lines.append("- Batches archived under `scripts/cybergym-batches/`.")
    lines.append("")
    SUMMARY_MD.write_text("\n".join(lines))


def main() -> int:
    BATCH_DIR.mkdir(parents=True, exist_ok=True)
    all_tasks = load_task_ids()
    prog = load_progress()
    prog = seed_progress_from_known_rounds(prog, all_tasks)

    # Apply skips
    for tid in SKIP_IDS:
        if tid in all_tasks and tid not in prog["skipped_tasks"]:
            prog["skipped_tasks"].append(tid)
    save_progress(prog)

    log(
        f"orchestrator start planned={len(all_tasks)} "
        f"completed={len(prog['completed_tasks'])} skipped={prog['skipped_tasks']}"
    )
    log(f"completed so far: {prog['completed_tasks']}")

    wait_for_compare()
    # Re-seed after wait (round2 may have just finished)
    prog = seed_progress_from_known_rounds(prog, all_tasks)
    # If live report has full round2, archive as batch2
    live = SCRIPTS / "cybergym-assisted-vs-interactive-report.json"
    if live.exists():
        try:
            rep = json.loads(live.read_text())
            tids = rep.get("task_ids") or []
            if set(tids) == {
                "arvo:1461",
                "arvo:65212",
                "arvo:781",
                "arvo:65530",
                "arvo:64574",
            }:
                dst = BATCH_DIR / "batch2-report.json"
                if not dst.exists():
                    shutil.copy2(live, dst)
                    log(f"archived round2 report -> {dst}")
                    for tid in tids:
                        if tid not in prog["completed_tasks"]:
                            prog["completed_tasks"].append(tid)
                    save_progress(prog)
        except Exception as exc:  # noqa: BLE001
            log(f"round2 archive note: {exc}")

    # Round1 already archived
    r1 = SCRIPTS / "cybergym-report-round1-hub.json"
    if r1.exists() and not (BATCH_DIR / "batch1-report.json").exists():
        shutil.copy2(r1, BATCH_DIR / "batch1-report.json")

    batch_num = len(prog.get("batches") or []) + 1
    # If batches empty but we have batch1/2 files, set batch_num accordingly
    existing_batch_files = list(BATCH_DIR.glob("batch*-report.json"))
    if existing_batch_files:
        nums = []
        for p in existing_batch_files:
            try:
                nums.append(int(p.name.split("batch")[1].split("-")[0]))
            except Exception:
                pass
        if nums:
            batch_num = max(batch_num, max(nums) + 1)

    while True:
        wait_for_compare()
        prog = load_progress()
        done = set(prog.get("completed_tasks") or []) | set(prog.get("skipped_tasks") or [])
        remaining = [t for t in all_tasks if t not in done]
        log(f"remaining={len(remaining)} {remaining[:10]}{'...' if len(remaining)>10 else ''}")
        if not remaining:
            break
        batch = remaining[:BATCH_SIZE]
        rec = run_batch(batch_num, batch, prog)
        log(f"batch {batch_num} done: {json.dumps({k: rec.get(k) for k in ('ready','compare_rc','error') if k in rec or True}, default=str)}")
        batch_num += 1
        # safety: if batch made no progress, avoid infinite loop
        prog = load_progress()
        done2 = set(prog.get("completed_tasks") or []) | set(prog.get("skipped_tasks") or [])
        if not (done2 - done) and not rec.get("ready"):
            log("no progress and no ready tasks — stopping")
            break

    prog = load_progress()
    summary = build_summary(prog, all_tasks)
    SUMMARY_JSON.write_text(json.dumps(summary, indent=2) + "\n")
    write_summary_md(summary)
    log(f"FINAL summary -> {SUMMARY_JSON} / {SUMMARY_MD}")
    t = summary["totals"]
    log(
        f"assisted={t['assisted_solved']}/{t['evaluated']} "
        f"interactive={t['interactive_solved']}/{t['evaluated']} "
        f"both={t['both_solved']} neither={t['neither']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
