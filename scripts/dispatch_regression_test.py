"""通过 CLI 和平台边界验证薄调度器的接力行为。"""

import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(os.environ.get("DISPATCH_SCRIPT", "internal/hostedcontroller/assets/ctf-orchestrator/scripts/dispatch.py")).resolve()


class DispatchRegression(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="dispatch-regression-")
        self.addCleanup(self.temp.cleanup)
        self.ws = Path(self.temp.name)
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
    print(json.dumps(state.get("start", {})))
else:
    print("{}")
''')
        (self.ws / "platform.sh").write_text(
            f"exec {shlex.quote(sys.executable)} {shlex.quote(str(self.ws / 'platform-fixture.py'))} \"$@\"\n")

    def calls(self):
        return [json.loads(line) for line in (self.ws / "platform-calls.jsonl").read_text().splitlines()]

    def cli(self, *args):
        result = subprocess.run([sys.executable, str(SCRIPT), *args, "--ws", str(self.ws)],
                                text=True, capture_output=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def report(self, code, attempt, *, outcome="stalled", gained=0, foothold='""', milestone="继续未完成阶段", body=""):
        path = self.ws / f"graph/attempts/{code}/{attempt}.md"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f"---\nchallenge: {code}\nattempt: {attempt}\noutcome: {outcome}\n"
                        f"flags_gained: {gained}\nfoothold: {foothold}\nnext_milestone: {milestone}\n"
                        f"---\n## 试过的面\n{body}\n", encoding="utf-8")

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
        self.cli("harvest", "--dry")
        self.cli("harvest", "--dry")
        rec = self.ledger()["challenges"]["tch3-06"]
        self.assertEqual(rec["flags_correct"], 1)
        self.assertEqual(rec["state"], "partial")
        self.assertEqual(self.calls(), [["list"], ["list"]])
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
        self.cli("mark", "d001", "infra_dead")
        self.cli("harvest", "--dry")
        self.assertIn(["abandon", "tch3-06", "slot-release"], self.calls())

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
            self.report("tch3-06", count, outcome="budget_stop", gained=1)
            self.challenges[0]["correct_flag_count"] = count
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
