#!/usr/bin/env python3
"""Download CyberGym level1 data for a fixed 30-task subset via hf-mirror.

Disk-conscious:
  - level1 files only: description.txt + repo-vul.tar.gz (not full repo-fix / patch)
  - Docker images pulled separately and sequentially (optional --pull-images)
  - Downloads via direct HTTPS from HF_ENDPOINT (default https://hf-mirror.com)
    — more reliable than huggingface_hub SDK which may hang on metadata

Usage:
  export HF_ENDPOINT=https://hf-mirror.com
  python3 scripts/cybergym-download-level1-30.py
  python3 scripts/cybergym-download-level1-30.py --pull-images   # also docker pull vul+fix

Eval model (CyberGym compare defaults):
  PENTEST_RUNTIME_PROFILE_ID=e7962d2782982265cb60da4f293aac57
  # CyberGym · HUB · deepseek-v4-pro · max
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

os.environ.setdefault("HF_ENDPOINT", "https://hf-mirror.com")

ROOT = Path(
    os.environ.get(
        "CYBERGYM_DATA_ROOT",
        "/Users/benjamin/tools/validation-benchmarks/cybergym_data",
    )
)
TASKS_FILE = Path(
    os.environ.get(
        "CYBERGYM_LEVEL1_30_JSON",
        str(ROOT / "level1_30_tasks.json"),
    )
)
REPO_ID = "sunblaze-ucb/cybergym"
LEVEL1_FILES = ("description.txt", "repo-vul.tar.gz")


def hf_base() -> str:
    return os.environ.get("HF_ENDPOINT", "https://hf-mirror.com").rstrip("/")


def load_task_ids() -> list[str]:
    if not TASKS_FILE.exists():
        raise SystemExit(f"missing task list: {TASKS_FILE}")
    data = json.loads(TASKS_FILE.read_text())
    ids = data.get("task_ids") or []
    if len(ids) < 1:
        raise SystemExit("empty task_ids")
    return [str(x) for x in ids]


def arvo_id(task_id: str) -> str:
    kind, _, num = task_id.partition(":")
    if kind != "arvo" or not num:
        raise ValueError(f"only arvo tasks supported in this script, got {task_id}")
    return num


def mirror_url(repo_path: str) -> str:
    # https://hf-mirror.com/datasets/<repo>/resolve/main/<path>
    return f"{hf_base()}/datasets/{REPO_ID}/resolve/main/{repo_path}"


def download_file(url: str, dest: Path, timeout: int = 600) -> None:
    """Stream download with curl (follows redirects; robust for large tar.gz)."""
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_suffix(dest.suffix + ".part")
    # Prefer curl for resume + progress
    cmd = [
        "curl",
        "-fL",
        "--retry",
        "3",
        "--retry-delay",
        "2",
        "--connect-timeout",
        "20",
        "--max-time",
        str(timeout),
        "-o",
        str(tmp),
        url,
    ]
    # resume if partial exists
    if tmp.exists() and tmp.stat().st_size > 0:
        cmd.insert(1, "-C")
        cmd.insert(2, "-")
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        # fall back to urllib
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "cybergym-level1-30"})
            with urllib.request.urlopen(req, timeout=timeout) as resp, open(tmp, "wb") as out:
                while True:
                    chunk = resp.read(1024 * 1024)
                    if not chunk:
                        break
                    out.write(chunk)
        except Exception as exc:  # noqa: BLE001
            if tmp.exists():
                tmp.unlink(missing_ok=True)
            raise RuntimeError(
                f"curl rc={proc.returncode} stderr={proc.stderr[:300]!r}; urllib={exc}"
            ) from exc
    if not tmp.exists() or tmp.stat().st_size == 0:
        raise RuntimeError(f"empty download for {url}")
    tmp.replace(dest)


def download_level1(task_id: str) -> list[Path]:
    aid = arvo_id(task_id)
    out_dir = ROOT / "data" / "arvo" / aid
    out_dir.mkdir(parents=True, exist_ok=True)
    paths: list[Path] = []
    for name in LEVEL1_FILES:
        repo_path = f"data/arvo/{aid}/{name}"
        local = out_dir / name
        if local.exists() and local.stat().st_size > 0:
            print(f"  skip existing {local} ({local.stat().st_size} B)")
            paths.append(local)
            continue
        url = mirror_url(repo_path)
        print(f"  download {url}")
        # description small; tar.gz larger
        timeout = 120 if name.endswith(".txt") else 1800
        download_file(url, local, timeout=timeout)
        print(f"  ok {local} ({local.stat().st_size} B)")
        paths.append(local)
    return paths


def pull_images(task_ids: list[str], max_workers: int = 1) -> None:
    try:
        import docker
    except ImportError as exc:
        raise SystemExit("docker python package required for --pull-images") from exc

    client = docker.from_env()
    # base runner once
    print("pulling cybergym/oss-fuzz-base-runner:latest")
    try:
        client.images.pull("cybergym/oss-fuzz-base-runner", tag="latest")
    except Exception as e:  # noqa: BLE001
        print(f"  warn base-runner: {e}")

    for tid in task_ids:
        aid = arvo_id(tid)
        for mode in ("vul", "fix"):
            tag = f"{aid}-{mode}"
            image = f"n132/arvo:{tag}"
            # skip if present
            try:
                client.images.get(image)
                print(f"  skip existing {image}")
                continue
            except Exception:
                pass
            print(f"  pulling {image} ...")
            try:
                client.images.pull("n132/arvo", tag=tag)
                print(f"  ok {image}")
            except Exception as e:  # noqa: BLE001
                print(f"  FAIL {image}: {e}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--pull-images",
        action="store_true",
        help="Also docker-pull n132/arvo:<id>-vul and -fix for each task",
    )
    parser.add_argument(
        "--limit",
        type=int,
        default=0,
        help="Only first N task_ids (0 = all in list)",
    )
    args = parser.parse_args()

    print(f"HF_ENDPOINT={os.environ.get('HF_ENDPOINT')}")
    print(f"data root={ROOT}")
    task_ids = load_task_ids()
    if args.limit > 0:
        task_ids = task_ids[: args.limit]
    print(f"tasks={len(task_ids)}")

    ok = 0
    fail: list[str] = []
    for tid in task_ids:
        print(f"=== {tid} ===")
        try:
            download_level1(tid)
            ok += 1
        except Exception as e:  # noqa: BLE001
            print(f"  FAIL data: {e}")
            fail.append(tid)

    print(f"\ndata done ok={ok} fail={len(fail)} {fail}")

    if args.pull_images:
        print("\n=== docker images ===")
        pull_images([t for t in task_ids if t not in fail])

    # write ready list
    ready = []
    for tid in task_ids:
        if tid in fail:
            continue
        aid = arvo_id(tid)
        d = ROOT / "data" / "arvo" / aid
        if (d / "description.txt").exists() and (d / "repo-vul.tar.gz").exists():
            ready.append(tid)
    ready_path = ROOT / "level1_30_ready.json"
    ready_path.write_text(
        json.dumps(
            {
                "difficulty": "level1",
                "hf_endpoint": os.environ.get("HF_ENDPOINT"),
                "ready_count": len(ready),
                "task_ids": ready,
                "failed": fail,
            },
            indent=2,
        )
        + "\n"
    )
    print(f"ready list -> {ready_path} ({len(ready)} tasks)")
    return 0 if not fail else 1


if __name__ == "__main__":
    raise SystemExit(main())
