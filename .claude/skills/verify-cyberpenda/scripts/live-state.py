#!/usr/bin/env python3
"""Real API setup and bounded read-only Runtime observations for Steer checks."""

import argparse
import http.cookiejar
import json
from pathlib import Path
import time
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("run", type=Path)
parser.add_argument("command", choices=["setup", "snapshot", "wait", "steer"])
parser.add_argument("--session")
parser.add_argument("--label", default="state")
parser.add_argument("--state", choices=["busy", "idle"], default="idle")
parser.add_argument("--text", default="")
parser.add_argument("--not-request", default="")
parser.add_argument("--timeout", type=int, default=120)
args = parser.parse_args()
root = args.run.resolve()
owner = json.loads((root / "owner.json").read_text())
assert owner["contract"] == "verify-cyberpenda-v1" and owner["root"] == str(root)
run = json.loads((root / "run.json").read_text())
jar = http.cookiejar.MozillaCookieJar(str(root / "scratch" / "doctor.cookies"))
jar.load(ignore_discard=True)
client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))


def request(path, method="GET", body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(run["ui"] + path, data=data, method=method, headers={
        "Origin": run["ui"], "Referer": run["ui"] + "/", "Sec-Fetch-Site": "same-origin",
        "Content-Type": "application/json",
    })
    with client.open(req, timeout=15) as response:
        raw = response.read()
        return json.loads(raw) if raw else None


def write(label, data):
    if not label.replace("-", "").replace("_", "").isalnum():
        raise ValueError("Use a simple unique label")
    path = root / "evidence" / (label + ".json")
    with path.open("x", encoding="utf-8") as output:
        json.dump(data, output, indent=2, ensure_ascii=False)
    print(str(path))


if args.command == "setup":
    access = json.loads((root / "scratch" / "model-access.json").read_text())
    relay = json.loads((root / "scratch" / "relay.json").read_text())
    base = relay["base_url"]
    provider = request("/api/model-providers", "POST", {
        "name": "Steer verification local gateway",
        "endpoints": [{"protocol": "openai_chat_completions", "base_url": base},
                      {"protocol": "anthropic_messages", "base_url": base.removesuffix("/v1")}],
        "catalog": {"manual": [access["model"]], "default_model": access["model"]},
    })
    request("/api/credential-bindings", "PUT", {
        "credential_ref": provider["api_key_env"], "source": {"kind": "literal", "value": access["api_key"],
                                                               "destination_env": provider["api_key_env"]},
    })
    request("/api/skills/opt-outs/global", "PUT", {})
    write("model-provider-setup", {"provider": provider, "skills": "globally opted out for this isolated test",
                                    "runner": "sandbox", "host_browser": False, "blackboard_mode": "disabled"})
else:
    if not args.session:
        parser.error("--session is required")
    base = "/api/sessions/" + args.session
    deadline = time.monotonic() + args.timeout
    observations = []
    while True:
        try:
            state = request(base)
            transcript = request(base + "/transcript")
        except OSError as error:
            write(args.label + "-request-error", {"error": str(error), "observations": observations})
            raise SystemExit("Runtime observation request failed; preserve the instance before recovery")
        activity = state.get("runtime_activity", {})
        controls = state.get("runtime_controls", {})
        sample = {"at": time.time(), "runtime_activity": activity, "runtime_controls": controls,
                  "latest_continuation": state.get("latest_continuation"), "status": state.get("status")}
        if not observations or {k: v for k, v in sample.items() if k != "at"} != {k: v for k, v in observations[-1].items() if k != "at"}:
            observations.append(sample)
        assistant_text = "\n".join(entry.get("text", "") for entry in transcript.get("entries", [])
                                   if entry.get("role") == "assistant" and entry.get("kind") == "message")
        if args.command == "steer":
            settled = (controls.get("native_steer_request_id", "") != args.not_request
                       and controls.get("native_steer_state") in ("applied", "failed", "action_required"))
        elif args.command == "snapshot":
            settled = True
        else:
            settled = (activity.get("liveness") == "live" and activity.get("turn_activity") == args.state
                       and (not args.text or args.text in assistant_text)
                       and controls.get("native_steer_state") not in ("pending", "dispatch_started"))
        if settled:
            write(args.label, {"owner": state, "transcript": transcript, "timeline": request(base + "/timeline"),
                               "observations": observations})
            print(json.dumps({"runtime_activity": activity, "runtime_controls": controls}, ensure_ascii=False))
            break
        if time.monotonic() >= deadline:
            write(args.label + "-timeout", {"owner": state, "transcript": transcript, "timeline": request(base + "/timeline"),
                                             "observations": observations})
            raise SystemExit("Runtime did not reach the required state before the deadline")
        time.sleep(0.5)
