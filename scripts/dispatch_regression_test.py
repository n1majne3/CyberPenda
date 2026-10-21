"""通过 CLI 和平台边界验证薄调度器的接力行为。"""

import json
import os
from pathlib import Path
import shlex
import subprocess
import socketserver
import threading
import sys
import tempfile
import unittest


SCRIPT = Path(os.environ.get("DISPATCH_SCRIPT", "internal/hostedcontroller/assets/ctf-orchestrator/scripts/dispatch.py")).resolve()


class DispatchRegression(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="dispatch-regression-")
        self.addCleanup(self.temp.cleanup)
        self.ws = Path(self.temp.name)
        self.env = os.environ.copy()
        self.challenges = [
            dict(unique_code="tch3-06", description="Chain", difficulty="medium", total_score=1400,
                 flag_count=6, correct_flag_count=0, is_completed=False, container_status="stopped"),
            dict(unique_code="tch3-11", description="Fresh", difficulty="medium", total_score=800,
                 flag_count=1, correct_flag_count=0, is_completed=False, container_status="stopped"),
        ]
        self.write_json("challenges.json", {"challenges": self.challenges})
        self.cli("init", "--challenges", str(self.ws / "challenges.json"))

    def write_json(self, name, value):
        path = self.ws / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value), encoding="utf-8")

    def ledger(self):
        return json.loads((self.ws / "graph/ledger.json").read_text())

    def save(self, ledger):
        self.write_json("graph/ledger.json", ledger)

    def platform(self):
        self.write_json("platform-state.json", {"challenges": self.challenges})
        (self.ws / "platform-fixture.py").write_text('''import json, pathlib, sys
root = pathlib.Path(__file__).parent
args = sys.argv[1:]
with (root / "platform-calls.jsonl").open("a") as f:
    f.write(json.dumps(args) + "\\n")
state = json.loads((root / "platform-state.json").read_text())
if args[0] in state.get("fail", []):
    sys.exit(1)
if args[0] == "list":
    print(json.dumps({"challenges": state["challenges"]}))
elif args[0] == "start":
    if "after_start" in state:
        state["challenges"] = state["after_start"]
        (root / "platform-state.json").write_text(json.dumps(state))
    if state.get("start_timeout"):
        sys.stderr.write("context deadline exceeded")
        sys.exit(1)
    for item in state["challenges"]:
        if item["unique_code"] == args[1] and state.get("start", {}).get("container_addr"):
            item.update(container_status="available", container_addr=state["start"]["container_addr"])
    (root / "platform-state.json").write_text(json.dumps(state))
    print(json.dumps(state.get("start", {})))
elif args[0] == "hint":
    print(json.dumps({"hint": "Inspect the second-stage export", "hint_cost_radio": 0.1}))
else:
    if args[0] in ("close", "abandon"):
        for item in state["challenges"]:
            if item["unique_code"] == args[1]:
                item.update(container_status="stopped", container_addr=[])
        (root / "platform-state.json").write_text(json.dumps(state))
    print("{}")
''')
        (self.ws / "platform.sh").write_text(
            f"exec {shlex.quote(sys.executable)} {shlex.quote(str(self.ws / 'platform-fixture.py'))} \"$@\"\n")

    def calls(self):
        return [json.loads(line) for line in (self.ws / "platform-calls.jsonl").read_text().splitlines()]

    def cli(self, *args):
        result = subprocess.run([sys.executable, str(SCRIPT), *args, "--ws", str(self.ws)],
                                text=True, capture_output=True, timeout=15, env=self.env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def rejected(self, *args):
        result = subprocess.run([sys.executable, str(SCRIPT), *args, "--ws", str(self.ws)],
                                text=True, capture_output=True, timeout=15, env=self.env)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stderr

    def runtime(self):
        from http.server import BaseHTTPRequestHandler
        from urllib.parse import parse_qs, urlsplit
        self.runtime_state = dict(agent_id="native-12", status="running", settled=False, runtime_id="runtime-1")
        self.runtime_states = {"native-12": self.runtime_state}
        states = self.runtime_states
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                state = states[parse_qs(urlsplit(self.path).query)["id"][0]]
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps(state).encode())
            def log_message(self, *args):
                pass
        # Use a short socket path: macOS limits Unix socket paths to 104 bytes.
        self.runtime_temp = tempfile.TemporaryDirectory(prefix="cp-", dir="/tmp")
        self.addCleanup(self.runtime_temp.cleanup)
        socket_path = str(Path(self.runtime_temp.name) / "runtime.sock")
        server = socketserver.UnixStreamServer(socket_path, Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)
        self.env.update(CYBERPENDA_HOSTED_PI="1", CYBERPENDA_PI_CONTROL_SOCKET=socket_path)

    def test_finish_requires_the_bound_runtime_to_settle(self):
        self.runtime()
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.cli("bind", "d001", "native-12")
        self.rejected("finish", "d001", "--agent-id", "native-12", "--status", "failed")
        self.runtime_state.update(status="stopped")
        self.rejected("finish", "d001", "--agent-id", "native-12", "--status", "stopped")
        self.assertFalse(self.ledger()["dispatches"]["d001"].get("terminal_at"))
        self.runtime_state.update(settled=True, runtime_id="another-runtime")
        self.rejected("finish", "d001", "--agent-id", "native-12", "--status", "stopped")
        self.runtime_state.update(runtime_id="runtime-1")
        self.cli("finish", "d001", "--agent-id", "native-12", "--status", "stopped")
        self.assertEqual(self.ledger()["dispatches"]["d001"]["terminal_status"], "stopped")

    def test_wait_returns_for_any_settled_execute_without_a_report(self):
        self.runtime()
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.cli("bind", "d001", "native-12")
        self.runtime_states["native-13"] = dict(agent_id="native-13", status="completed", settled=True, runtime_id="runtime-1")
        self.cli("assemble", "tch3-11")
        self.cli("take")
        self.cli("bind", "d002", "native-13")
        output = self.cli("wait", "--timeout", "1")
        self.assertIn("d002", output)
        self.assertIn("completed", output)
        self.assertFalse(self.ledger()["dispatches"]["d001"].get("terminal_at"))

    def test_wait_does_not_hold_the_dispatcher_lock(self):
        waiting = subprocess.Popen([sys.executable, str(SCRIPT), "wait", "--timeout", "10", "--ws", str(self.ws)],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=self.env)
        self.addCleanup(lambda: waiting.poll() is None and waiting.kill())
        # The public wait command reports readiness before blocking.
        self.assertEqual(waiting.stdout.readline().strip(), "waiting")
        self.cli("assemble", "tch3-06")
        out, err = waiting.communicate(timeout=3)
        self.assertEqual(waiting.returncode, 0, err)
        self.assertIn("READY", out)

    def test_interim_report_cannot_release_a_live_execute(self):
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.platform()
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.report("tch3-06", 1, foothold="none", body="进行中")
        self.cli("harvest", "--dry")
        self.assertEqual(self.ledger()["dispatches"]["d001"]["state"], "dispatched")
        self.assertEqual(self.calls(), [["list"]])
        self.cli("bind", "d001", "native-12")
        self.rejected("finish", "d001", "--agent-id", "wrong-id", "--status", "completed")
        self.rejected("mark", "d001", "infra_dead")
        self.cli("finish", "d001", "--agent-id", "native-12", "--status", "completed")
        self.report("tch3-06", 1, foothold="错误的迟到写入", body="SENTINEL WAF")
        self.cli("harvest", "--dry")
        self.assertEqual(self.ledger()["dispatches"]["d001"]["state"], "done")
        self.assertEqual(self.ledger()["challenges"]["tch3-06"]["foothold"], "")
        self.assertIn(["abandon", "tch3-06", "slot-release"], self.calls())

    def test_manual_dispatch_requires_a_current_instance_and_exclusive_execute(self):
        self.platform()
        self.rejected("assemble", "tch3-06")

        self.assertEqual(self.ledger()["dispatches"], {})
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.rejected("assemble", "tch3-06")
        self.challenges[0].update(correct_flag_count=6, is_completed=True)
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.rejected("assemble", "tch3-06")

    def test_start_timeout_reconciles_server_success_without_release_or_retry(self):
        self.platform()
        after = json.loads(json.dumps(self.challenges))
        after[0].update(container_status="available", container_addr=["192.0.2.11:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges, "after_start": after,
                                                "start_timeout": True})
        self.cli("harvest", "--quota", "1")
        self.assertIn("tch3-06", self.cli("take"))
        self.cli("harvest", "--quota", "1")
        self.assertEqual([c for c in self.calls() if c[0] != "list"], [["start", "tch3-06"]])

    def test_pending_start_survives_later_reconciliation_without_counting_infra_death(self):
        self.platform()
        after = json.loads(json.dumps(self.challenges))
        after[0]["container_status"] = "pending"
        self.write_json("platform-state.json", {"challenges": self.challenges, "after_start": after, "start_timeout": True})
        self.cli("harvest", "--quota", "1")
        self.cli("harvest", "--quota", "1")
        self.assertEqual(self.cli("take"), "")
        after[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": after})
        self.cli("harvest", "--quota", "1")
        self.assertIn("tch3-06", self.cli("take"))
        self.assertEqual(self.ledger()["challenges"]["tch3-06"]["infra_deaths"], 0)
        self.assertEqual([c for c in self.calls() if c[0] != "list"], [["start", "tch3-06"]])

    def test_instance_change_stops_execute_and_excludes_its_report_and_facts(self):
        self.platform()
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.cli("bind", "d001", "native-12")
        self.report("tch3-06", 1, body="WRONG_TARGET_REPORT")
        instance = self.ledger()["dispatches"]["d001"]["instance"]["generation"]
        (self.ws / "graph/facts/fact_001.md").write_text(
            f"---\nchallenge: tch3-06\ndispatch: d001\ninstance: {instance}\n---\nWRONG_TARGET_FACT\n")
        self.challenges[0]["container_status"] = "stopped"
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("harvest", "--dry")
        self.assertIn("d001", (self.ws / "graph/outbox/STOP.tsv").read_text())
        self.assertNotIn("abandon", str(self.calls()))
        self.cli("finish", "d001", "--agent-id", "native-12", "--status", "stopped")
        self.cli("harvest", "--dry")
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.20:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        prompt = (self.ws / self.ledger()["dispatches"]["d002"]["prompt"]).read_text()
        self.assertNotIn("WRONG_TARGET", prompt)
        self.assertIn("192.0.2.20:80", prompt)

    def test_stagnation_requests_a_hint_decision_and_hands_over_the_saved_response(self):
        self.platform()
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.cli("bind", "d001", "native-12")
        ledger = self.ledger()
        ledger["dispatches"]["d001"].update(created=1, last_progress_at=1, hard_stop=9999999999)
        self.save(ledger)
        (self.ws / "graph/attempts/tch3-06/1.started").touch()
        self.cli("harvest", "--dry")
        self.assertIn("hint", (self.ws / "graph/escalations/QUEUE.md").read_text())
        self.assertEqual([c for c in self.calls() if c[0] == "hint"], [])
        self.cli("decide", "e001", "--decision", "hint", "--reason", "卡在第二阶段，预期收益高于扣分")
        self.assertEqual((self.ws / "graph/escalations/QUEUE.md").read_text(), "")
        self.cli("decide", "e001", "--decision", "hint", "--reason", "重放同一决策")
        self.assertEqual([c for c in self.calls() if c[0] == "hint"], [["hint", "tch3-06"]])
        self.report("tch3-06", 1)
        self.cli("finish", "d001", "--agent-id", "native-12", "--status", "completed")
        self.cli("harvest", "--dry")
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        prompt = (self.ws / self.ledger()["dispatches"]["d002"]["prompt"]).read_text()
        self.assertIn("Inspect the second-stage export", prompt)
        self.assertIn("graph/hints/", prompt)

    def test_abandon_waits_for_runtime_and_remains_exhausted_without_a_report(self):
        self.platform()
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.cli("bind", "d001", "native-12")
        ledger = self.ledger()
        ledger["dispatches"]["d001"].update(last_progress_at=1, hard_stop=9999999999)
        self.save(ledger)
        self.cli("harvest", "--dry")
        self.cli("decide", "e001", "--decision", "abandon", "--reason", "收益不足")
        self.assertNotIn("abandon", str(self.calls()))
        self.cli("finish", "d001", "--agent-id", "native-12", "--status", "stopped")
        self.cli("harvest", "--dry")
        self.assertEqual(self.ledger()["challenges"]["tch3-06"]["state"], "exhausted")
        self.assertIn(["abandon", "tch3-06", "slot-release"], self.calls())

    def report(self, code, attempt, *, outcome="stalled", gained=0, foothold='""', milestone="继续未完成阶段", body=""):
        path = self.ws / f"graph/attempts/{code}/{attempt}.md"
        path.parent.mkdir(parents=True, exist_ok=True)
        origin = ""
        for did, d in self.ledger()["dispatches"].items():
            if d["code"] == code and d["attempt"] == attempt and d.get("instance", {}).get("generation"):
                origin = f"dispatch: {did}\ninstance: {d['instance']['generation']}\n"
        path.write_text(f"---\nchallenge: {code}\nattempt: {attempt}\noutcome: {outcome}\n{origin}"
                        f"flags_gained: {gained}\nfoothold: {foothold}\nnext_milestone: {milestone}\n"
                        f"---\n## 试过的面\n{body}\n", encoding="utf-8")

    def test_relay_keeps_nested_excluded_sections(self):
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.report("tch3-06", 1, body="### 实例归属\n已确认实例。\n### 排除项\n不要重复扫描登录页。\n## 后续计划\nNEXT_SECTION")
        self.cli("harvest", "--dry")
        self.cli("assemble", "tch3-06")
        prompt = (self.ws / self.ledger()["dispatches"]["d002"]["prompt"]).read_text()
        self.assertIn("不要重复扫描登录页。", prompt)
        self.assertNotIn("NEXT_SECTION", prompt)

    def test_missing_report_does_not_make_a_retry_outrank_a_fresh_challenge(self):
        self.cli("assemble", "tch3-06")
        self.cli("take")
        ledger = self.ledger()
        ledger["dispatches"]["d001"].update(created=1, hard_stop=1)
        self.save(ledger)
        marker = self.ws / "graph/attempts/tch3-06/1.started"
        marker.touch()
        self.cli("harvest", "--quota", "1")
        ready = self.cli("take")
        self.assertIn("tch3-11", ready)
        self.assertNotIn("tch3-06", ready)

    def test_empty_foothold_does_not_grant_a_siege_budget(self):
        self.cli("assemble", "tch3-11")
        self.cli("take")
        self.report("tch3-11", 1)
        self.cli("harvest", "--dry")
        self.cli("assemble", "tch3-11")
        dispatch = self.ledger()["dispatches"]["d002"]
        self.assertFalse(dispatch["siege"])
        self.assertEqual(dispatch["hard_stop"] - dispatch["created"], 40 * 60)

    def test_retry_receives_own_facts_and_latest_correction(self):
        self.cli("assemble", "tch3-06")
        self.report("tch3-06", 1, body="旧记录。" * 1000)
        ledger = self.ledger()
        ledger["challenges"]["tch3-06"]["attempt"] = 2
        self.save(ledger)
        self.report("tch3-06", 2, body="LATEST_CORRECTION: 旧会话已失效，使用新入口")
        (self.ws / "graph/facts/fact_001.md").write_text(
            "---\nid: fact_001\nchallenge: tch3-06\ntitle: 已完成第一阶段\n---\nOWN_FACT: 数据库账号已确认\n")
        self.cli("assemble", "tch3-06")
        prompt = (self.ws / self.ledger()["dispatches"]["d002"]["prompt"]).read_text()
        self.assertIn("OWN_FACT", prompt)
        self.assertIn("graph/facts/fact_001.md", prompt)
        self.assertIn("LATEST_CORRECTION", prompt)

    def test_platform_count_keeps_a_chain_partial_and_harvest_is_idempotent(self):
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.platform()
        self.challenges[0]["correct_flag_count"] = 1
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.report("tch3-06", 1, outcome="solved", gained=1, milestone='""', body='{"correct": true}')
        self.cli("bind", "d001", "native-1")
        self.cli("finish", "d001", "--agent-id", "native-1", "--status", "completed")
        self.cli("harvest", "--dry")
        self.cli("harvest", "--dry")
        rec = self.ledger()["challenges"]["tch3-06"]
        self.assertEqual(rec["flags_correct"], 1)
        self.assertEqual(rec["state"], "partial")
        self.assertEqual(self.calls(), [["list"], ["list"]])
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("assemble", "tch3-06")
        prompt = (self.ws / self.ledger()["dispatches"]["d002"]["prompt"]).read_text()
        self.assertIn("1/6", prompt)
        self.assertIn("第 2 枚", prompt)
        self.challenges[0].update(correct_flag_count=6, is_completed=True)
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("harvest", "--dry")
        self.assertEqual(self.ledger()["challenges"]["tch3-06"]["state"], "solved")

    def test_restart_passes_all_current_addresses_and_invalidates_live_foothold(self):
        self.platform()
        ledger = self.ledger()
        ledger["challenges"]["tch3-11"]["state"] = "exhausted"
        ledger["challenges"]["tch3-06"].update(
            state="partial", attempt=1, addr="192.0.2.1:80", foothold="旧会话可用")
        self.save(ledger)
        self.write_json("platform-state.json", {"challenges": self.challenges, "start": {
            "container_addr": ["http://192.0.2.10:8080", "192.0.2.11:22"]}})
        self.cli("harvest", "--quota", "1")
        dispatch = self.ledger()["dispatches"]["d001"]
        prompt = (self.ws / dispatch["prompt"]).read_text()
        self.assertIn("http://192.0.2.10:8080", prompt)
        self.assertIn("192.0.2.11:22", prompt)
        self.assertNotIn("192.0.2.1:80", prompt)
        self.assertNotIn("上一棒的立足点仍有效", prompt)
        self.assertFalse(dispatch["siege"])

    def test_failed_release_keeps_the_slot_and_never_restarts_it(self):
        self.platform()
        self.challenges[0].update(container_status="available", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges, "fail": ["abandon"]})
        self.cli("harvest", "--quota", "1")
        self.assertIsNotNone(self.ledger()["challenges"]["tch3-06"]["instance"])
        self.assertEqual(self.calls(), [["list"], ["abandon", "tch3-06", "slot-release"]])
        self.write_json("platform-state.json", {"challenges": self.challenges, "fail": ["list"]})
        self.cli("harvest", "--quota", "1")
        self.assertEqual(self.calls()[-1], ["list"])
        self.assertEqual(len(self.calls()), 3)

    def test_catalog_prefix_does_not_make_unrelated_challenges_a_family(self):
        (self.ws / "graph/facts/fact_001.md").write_text(
            "---\nchallenge: tch3-06\ntitle: unrelated\n---\nUNRELATED_TECHNIQUE\n")
        self.cli("assemble", "tch3-11")
        prompt = (self.ws / self.ledger()["dispatches"]["d001"]["prompt"]).read_text()
        self.assertNotIn("UNRELATED_TECHNIQUE", prompt)

    def test_timeout_waits_for_execute_stop_before_releasing_the_instance(self):
        self.cli("assemble", "tch3-06")
        self.cli("take")
        ledger = self.ledger()
        ledger["dispatches"]["d001"].update(created=1, hard_stop=1)
        self.save(ledger)
        (self.ws / "graph/attempts/tch3-06/1.started").touch()
        self.platform()
        self.challenges[0].update(container_status="running", container_addr=["192.0.2.10:80"])
        self.write_json("platform-state.json", {"challenges": self.challenges})
        self.cli("harvest", "--quota", "1")
        self.assertEqual(self.calls(), [["list"]])
        self.assertIn("d001", (self.ws / "graph/outbox/STOP.tsv").read_text())
        self.cli("bind", "d001", "native-1")
        self.cli("finish", "d001", "--agent-id", "native-1", "--status", "stopped")
        self.cli("harvest", "--dry")
        self.assertIn(["abandon", "tch3-06", "slot-release"], self.calls())
        self.assertEqual(self.ledger()["challenges"]["tch3-06"]["infra_deaths"], 1)

    def test_completed_platform_challenge_is_not_started_and_ready_events_are_consumed(self):
        self.platform()
        self.challenges[0].update(correct_flag_count=6, is_completed=True)
        self.write_json("platform-state.json", {"challenges": self.challenges, "start": {
            "container_addr": ["192.0.2.11:80"]}})
        self.cli("harvest", "--quota", "1")
        self.assertEqual(self.calls(), [["list"], ["start", "tch3-11"]])
        ready = self.ws / "graph/outbox/READY.tsv"
        self.assertIn("tch3-11", ready.read_text())
        self.cli("take")
        self.assertEqual(ready.read_text(), "")

    def test_chain_with_new_flags_keeps_receiving_the_next_stage(self):
        ledger = self.ledger()
        ledger["challenges"]["tch3-11"]["state"] = "exhausted"
        self.save(ledger)
        self.platform()
        self.write_json("platform-state.json", {"challenges": self.challenges, "start": {
            "container_addr": ["192.0.2.10:80"]}})
        self.cli("harvest", "--quota", "1")
        for count in range(1, 6):
            self.cli("take")
            self.cli("bind", f"d{count:03}", f"native-{count}")
            self.report("tch3-06", count, outcome="budget_stop", gained=1)
            self.cli("finish", f"d{count:03}", "--agent-id", f"native-{count}", "--status", "completed")
            self.challenges[0].update(correct_flag_count=count, container_status="available", container_addr=["192.0.2.10:80"])
            self.write_json("platform-state.json", {"challenges": self.challenges, "start": {
                "container_addr": ["192.0.2.10:80"]}})
            self.cli("harvest", "--quota", "1")
            rec = self.ledger()["challenges"]["tch3-06"]
            self.assertEqual(rec["state"], "running", f"新获得第 {count} 枚 Flag 后被错误封存")
            self.assertEqual(rec["attempt"], count + 1)

    def test_absent_challenge_is_not_started_from_a_stale_ledger(self):
        self.platform()
        self.write_json("platform-state.json", {"challenges": []})
        self.cli("harvest", "--quota", "1")
        self.assertEqual(self.calls(), [["list"]])

    def test_platform_transition_keeps_its_slot_until_stopped(self):
        self.platform()
        for status in ("pending", "stop_pending"):
            self.challenges[0]["container_status"] = status
            self.write_json("platform-state.json", {"challenges": self.challenges})
            self.cli("harvest", "--quota", "1")
        self.assertEqual(self.calls(), [["list"], ["list"]])

    def test_interrupted_retry_retains_the_unfinished_milestone(self):
        ledger = self.ledger()
        ledger["challenges"]["tch3-06"].update(attempt=1, milestone="RESUME_DATABASE_STAGE", flags_correct=1)
        self.save(ledger)
        self.cli("assemble", "tch3-06")
        self.cli("take")
        self.cli("mark", "d001", "infra_dead")
        self.cli("assemble", "tch3-06")
        prompt = (self.ws / self.ledger()["dispatches"]["d002"]["prompt"]).read_text()
        self.assertIn("RESUME_DATABASE_STAGE", prompt)
        self.assertIn("1/6", prompt)


if __name__ == "__main__":
    unittest.main()
