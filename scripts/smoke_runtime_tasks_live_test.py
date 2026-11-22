import importlib.util
import json
import unittest
from pathlib import Path
from unittest import mock

SCRIPT_PATH = Path(__file__).resolve().parent / "smoke-runtime-tasks-live.py"
SPEC = importlib.util.spec_from_file_location("smoke_runtime_tasks_live", SCRIPT_PATH)
SMOKE = importlib.util.module_from_spec(SPEC)
assert SPEC is not None and SPEC.loader is not None
SPEC.loader.exec_module(SMOKE)


class SmokeRuntimeTasksLiveTest(unittest.TestCase):
    def test_build_smoke_fgs_payload_creates_valid_goal_step_fact_sequence(self) -> None:
        fact_key, raw = SMOKE.build_smoke_fgs_payload("codex", "Codex runtime FGS smoke passed")
        self.assertEqual(fact_key, "fact:smoke-codex")
        body = json.loads(raw)
        ops = body["operations"]
        self.assertEqual(
            [op["op"] for op in ops],
            ["goal.create", "step.create", "fact.append", "step.transition", "goal.transition"],
        )
        self.assertEqual(ops[0]["key"], "goal:smoke-codex")
        self.assertEqual(ops[1]["key"], "step:smoke-codex")
        self.assertEqual(ops[1]["goal"], "goal:smoke-codex")
        self.assertEqual(ops[2]["key"], "fact:smoke-codex")
        self.assertEqual(ops[2]["step"], "step:smoke-codex")
        self.assertEqual(ops[2]["summary"], "Codex runtime FGS smoke passed")
        self.assertEqual(ops[3]["outputs"], ["fact:smoke-codex"])
        self.assertEqual(ops[4]["facts"], ["fact:smoke-codex"])

    def test_smoke_goal_does_not_reference_retired_fact_mcp_api(self) -> None:
        text = SCRIPT_PATH.read_text(encoding="utf-8")
        self.assertNotIn("upsert_project_fact", text)
        self.assertNotIn("PENTEST_MCP_URL", text)
        self.assertNotIn("/facts/", text)
        self.assertIn("pentestctl working-graph emit --input -", SMOKE.SMOKE_GOAL)

    def test_find_fgs_fact_paginates_v2_fgs_endpoint(self) -> None:
        calls: list[tuple[str, str]] = []

        def fake_request(method: str, path: str, body=None) -> dict:
            calls.append((method, path))
            if "cursor=page-2" not in path:
                return {
                    "nodes": [{"type": "goal", "key": "goal:smoke-codex"}],
                    "next_cursor": "page-2",
                }
            return {
                "nodes": [
                    {"type": "fact", "key": "fact:smoke-codex", "summary": "Codex ok"},
                ],
                "next_cursor": "",
            }

        with mock.patch.object(SMOKE, "request", side_effect=fake_request):
            found = SMOKE.find_fgs_fact("proj-1", "fact:smoke-codex")

        self.assertEqual(found, {"type": "fact", "key": "fact:smoke-codex", "summary": "Codex ok"})
        self.assertEqual(
            calls,
            [
                ("GET", "/api/v2/projects/proj-1/fgs?limit=200"),
                ("GET", "/api/v2/projects/proj-1/fgs?limit=200&cursor=page-2"),
            ],
        )

    def test_wait_for_smoke_outcome_rechecks_fgs_when_task_completes(self) -> None:
        fact_results = [None, {"type": "fact", "key": "fact:smoke-pi", "summary": "Pi ok"}]

        def fake_request(method: str, path: str, body=None) -> dict:
            if path.endswith("/tasks/task-1"):
                return {"status": "completed"}
            if path.endswith("/tasks/task-1/events"):
                return {"events": []}
            raise AssertionError(f"unexpected request: {method} {path}")

        with (
            mock.patch.object(SMOKE, "find_fgs_fact", side_effect=fact_results),
            mock.patch.object(SMOKE, "request", side_effect=fake_request),
        ):
            ok, reason = SMOKE.wait_for_smoke_outcome("proj-1", "task-1", "fact:smoke-pi")

        self.assertTrue(ok)
        self.assertEqual(reason, "fact")

    def test_project_and_task_payloads_include_required_daemon_fields(self) -> None:
        project_body = SMOKE.build_smoke_project_payload()
        self.assertEqual(project_body["kind"], "pentest")
        self.assertEqual(project_body["name"], "Runtime FGS Live Smoke")

        sandbox_task = SMOKE.build_smoke_task_payload("goal", "prof-1", "sandbox")
        self.assertEqual(sandbox_task["type"], "pentest")
        self.assertEqual(sandbox_task["runner"], "sandbox")
        self.assertEqual(sandbox_task["run_controls"], {"blackboard_mode": "working_graph"})

        host_task = SMOKE.build_smoke_task_payload("goal", "prof-2", "host")
        self.assertEqual(host_task["type"], "pentest")
        self.assertEqual(host_task["runner"], "host")
        self.assertTrue(host_task["run_controls"]["host_activated"])

    def test_pi_profile_custom_args_do_not_redefine_structured_provider(self) -> None:
        profiles = SMOKE.build_smoke_profiles("https://example.invalid", "tok", "oa")
        _, provider, fields = profiles["pi"]
        self.assertEqual(provider, "pi")
        self.assertNotIn("--provider", fields["custom_args"])
        self.assertNotIn("--model", fields["custom_args"])


if __name__ == "__main__":
    unittest.main()
