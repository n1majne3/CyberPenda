#!/usr/bin/env python3
"""Own one Windows verification instance; keep evidence after scratch cleanup."""

import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.request
import uuid

REPO = Path(__file__).resolve().parents[4]
CONTRACT = "verify-cyberpenda-v1"


def write_json(path, value):
    with path.open("x", encoding="utf-8") as output:
        json.dump(value, output, indent=2)


def owner(root):
    value = json.loads((root / "owner.json").read_text(encoding="utf-8"))
    if value.get("contract") != CONTRACT or value.get("root") != str(root):
        raise RuntimeError("Not a run created by this helper")
    return value


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def child_env():
    names = {
        "SYSTEMROOT", "WINDIR", "PATH", "TEMP", "TMP", "USERPROFILE",
        "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES",
        "PROGRAMFILES(X86)", "COMSPEC", "PATHEXT", "HOME",
        "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
    }
    return {key: value for key, value in os.environ.items() if key.upper() in names}


def log_lines(process, target):
    with target.open("x", encoding="utf-8") as output:
        for line in process.stdout:
            if "Operator token:" in line or "Blackboard operator access:" in line:
                line = "[verification: generated Operator Token omitted]\n"
            line = re.sub(r"([?&]token=)[^\s\"']+", r"\1[redacted]", line)
            output.write(line)
            output.flush()


def http_client(root, load=True):
    jar = http.cookiejar.MozillaCookieJar(str(root / "scratch" / "doctor.cookies"))
    if load:
        jar.load(ignore_discard=True)
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar)), jar


def request(client, url, method="GET"):
    origin = url.split("/", 3)[:3]
    req = urllib.request.Request(url, method=method, headers={
        "Origin": "/".join(origin), "Referer": "/".join(origin) + "/",
        "Sec-Fetch-Site": "same-origin",
    })
    with client.open(req, timeout=10) as response:
        return response.read()


def doctor(root):
    owner(root)
    if (root / "cleanup.json").exists():
        raise RuntimeError("This instance is stopped")
    run = json.loads((root / "run.json").read_text(encoding="utf-8"))
    client, _ = http_client(root)
    for base in [run["api"], run["ui"]]:
        health = json.loads(request(client, base + "/health"))
        if health["version"] != run["version"] or Path(health["runner"]["runtime_root"]).resolve() != root / "scratch" / "runtime":
            raise RuntimeError("Health belongs to another instance")
        if health["database"]["status"] != "ok":
            raise RuntimeError("Database is not ready")
    ports = subprocess.check_output(["netstat.exe", "-ano", "-p", "tcp"], text=True)
    for port, pid in [(run["api_port"], run["daemon_pid"]), (run["ui_port"], run["vite_pid"])]:
        if not any(len(row := line.split()) >= 5 and row[1] == f"127.0.0.1:{port}" and row[3] == "LISTENING" and row[4] == str(pid) for line in ports.splitlines()):
            raise RuntimeError(f"Port {port} is not owned by the recorded process")
    projects = json.loads(request(client, run["ui"] + "/api/projects"))
    if "projects" not in projects or not isinstance(projects["projects"], (list, type(None))):
        raise RuntimeError("Authenticated Project list is not usable")
    page = request(client, run["ui"] + "/").decode("utf-8")
    if "/src/main.tsx" not in page:
        raise RuntimeError("Current Vite entry is not served")
    request(client, run["ui"] + "/src/main.tsx")
    request(client, run["ui"] + "/src/index.css")
    return {"status": "ready", "version": run["version"], "ui": run["ui"], "api": run["api"], "evidence": run["evidence"], "project_count": len(projects.get("projects") or [])}


def launch(root, sandbox_image=""):
    if os.name != "nt":
        raise RuntimeError("This helper requires Windows")
    root.mkdir(parents=True, exist_ok=False)
    scratch = root / "scratch"
    evidence = root / "evidence"
    scratch.mkdir()
    evidence.mkdir()
    write_json(root / "owner.json", {"contract": CONTRACT, "root": str(root), "supervisor_pid": os.getpid()})
    write_json(root / "browser.json", {})
    processes, readers = [], []
    failure = None

    def start(command, label, cwd):
        process = subprocess.Popen(command, cwd=cwd, env=child_env(), stdin=subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                                   encoding="utf-8", errors="replace", creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
        processes.append(process)
        reader = threading.Thread(target=log_lines, args=(process, evidence / f"{label}.log"))
        reader.start()
        readers.append(reader)
        return process

    try:
        version = "verify-" + uuid.uuid4().hex
        binary = scratch / "pentestd.exe"
        build = start(["go", "build", "-ldflags", f"-X main.version={version}", "-o", str(binary), "./cmd/pentestd"], "build", REPO)
        while build.poll() is None:
            if (root / "STOP").exists():
                raise RuntimeError("Stopped during build")
            time.sleep(0.2)
        if build.returncode:
            raise RuntimeError("Go build failed; see evidence/build.log")
        api_port, ui_port = free_port(), free_port()
        while ui_port == api_port:
            ui_port = free_port()
        api, ui = f"http://127.0.0.1:{api_port}", f"http://127.0.0.1:{ui_port}"
        daemon_command = [str(binary), "-addr", f"127.0.0.1:{api_port}", "-db", str(scratch / "pentest.db"),
                          "-runtime-root", str(scratch / "runtime"), "-artifact-root", str(scratch / "artifacts")]
        if sandbox_image:
            daemon_command += ["-sandbox-image", sandbox_image]
        daemon = start(daemon_command, "daemon", scratch)
        vite = start(["node", str(Path(__file__).with_name("vite.mjs")), str(REPO), str(scratch), str(ui_port), str(api_port)], "vite", REPO / "web")
        write_json(root / "run.json", {"version": version, "ui": ui, "api": api, "evidence": str(evidence),
                                     "api_port": api_port, "ui_port": ui_port, "daemon_pid": daemon.pid, "vite_pid": vite.pid,
                                     "repo": str(REPO), "session": version, "runtime_launch": "not exercised",
                                     "sandbox_image": sandbox_image or "daemon default"})
        client, jar = http_client(root, load=False)
        deadline = time.monotonic() + 90
        while True:
            if daemon.poll() is not None or vite.poll() is not None:
                raise RuntimeError("A server exited before readiness; see evidence logs")
            if (root / "STOP").exists():
                raise RuntimeError("Stopped before readiness")
            try:
                request(client, ui + "/api/operator-session", method="POST")
                jar.save(ignore_discard=True)
                status = doctor(root)
                break
            except (OSError, ValueError, KeyError, RuntimeError):
                if time.monotonic() >= deadline:
                    raise RuntimeError("Instance did not become usable within 90 seconds")
                time.sleep(0.5)
        write_json(evidence / "launch.json", status)
        print(json.dumps(status), flush=True)
        while not (root / "STOP").exists():
            if daemon.poll() is not None or vite.poll() is not None:
                raise RuntimeError("A verification server exited")
            time.sleep(0.25)
    except BaseException as error:
        failure = str(error) or type(error).__name__
        raise
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                subprocess.run(["taskkill.exe", "/PID", str(process.pid), "/T", "/F"],
                               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                process.wait(timeout=10)
        for reader in readers:
            reader.join(timeout=10)
        # This exact directory was absent before launch and is generator-owned.
        shutil.rmtree(scratch)
        write_json(root / "cleanup.json", {"status": "stopped", "failure": failure,
                                          "pids": [process.pid for process in processes], "scratch_removed": True,
                                          "evidence": str(evidence)})


def stop(root):
    owner(root)
    marker = root / "STOP"
    if not marker.exists():
        marker.touch(exist_ok=False)
    deadline = time.monotonic() + 60
    while not (root / "cleanup.json").exists():
        if time.monotonic() >= deadline:
            raise RuntimeError("Supervisor did not confirm cleanup; do not kill by process name")
        time.sleep(0.25)
    print((root / "cleanup.json").read_text(encoding="utf-8"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["launch", "doctor", "stop"])
    parser.add_argument("run", type=Path, help="Absolute, new run directory for launch")
    parser.add_argument("--wait", type=int, default=0, help="Doctor readiness wait in seconds")
    parser.add_argument("--sandbox-image", default=os.getenv("VERIFY_SANDBOX_IMAGE", ""),
                        help="Sandbox image override for a real Runtime journey")
    args = parser.parse_args()
    root = args.run.resolve()
    if args.command == "launch":
        launch(root, args.sandbox_image.strip())
    elif args.command == "stop":
        stop(root)
    else:
        deadline = time.monotonic() + args.wait
        while True:
            try:
                print(json.dumps(doctor(root), indent=2))
                break
            except (OSError, ValueError, KeyError, RuntimeError):
                if (root / "cleanup.json").exists() or time.monotonic() >= deadline:
                    raise
                time.sleep(0.5)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"verification: {error}", file=sys.stderr)
        sys.exit(1)
