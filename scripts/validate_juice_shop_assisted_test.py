import importlib.util
import pathlib
import unittest


SCRIPT = pathlib.Path(__file__).with_name("validate-juice-shop-assisted-live.py")
SPEC = importlib.util.spec_from_file_location("assisted_live", SCRIPT)
assisted_live = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(assisted_live)


class AssistedJuiceShopValidationTest(unittest.TestCase):
    def test_launch_uses_assisted_mode_and_forbids_runtime_blackboard_writes(self):
        payload = assisted_live.build_launch_payload("profile-1", "sandbox")

        self.assertEqual(
            payload["run_controls"]["blackboard_conclusion_mode"], "assisted"
        )
        goal = payload["goal"].lower()
        self.assertIn("do not call", goal)
        self.assertIn("blackboard", goal)
        self.assertIn("do not finish", goal)

    def test_host_launch_requires_explicit_activation(self):
        payload = assisted_live.build_launch_payload("profile-1", "host")

        self.assertTrue(payload["run_controls"]["host_activated"])

    def test_applied_terminal_attempt_is_a_success_while_task_stays_live_idle(self):
        evidence = assisted_live.evaluate_observation(
            initial_revision=3,
            task={
                "status": "running",
                "runtime_activity": {"liveness": "live", "turn_activity": "idle"},
                "blackboard_conclusion": {
                    "mode": "assisted",
                    "state": "clean",
                    "applied_revision": 4,
                },
            },
            timeline={"items": [
                {"seq": 1, "type": "harness", "content": "Blackboard conclusion pending for work Turn work-1", "created_at": "2026-01-01T00:00:01Z"},
                {"seq": 2, "type": "harness", "content": "Blackboard Conclude Turn started", "created_at": "2026-01-01T00:00:02Z"},
                {"seq": 3, "type": "harness", "content": "Blackboard conclusion applied at revision 4", "created_at": "2026-01-01T00:00:04Z"},
            ]},
            events={"events": [{
                "kind": "blackboard_conclusion",
                "payload": {
                    "phase": "pending_detected",
                    "source_work_watermark": 2,
                    "semantic_persistence_watermark": 0,
                },
            }]},
            attempt_detail={
                "type": "attempt",
                "record": {"status": "succeeded", "summary": "Located the Score Board route."},
            },
        )

        self.assertTrue(evidence["passed"])
        self.assertEqual(evidence["coverage"]["completed_work_turns"], 1)
        self.assertEqual(evidence["coverage"]["covered_work_turns"], 1)
        self.assertEqual(evidence["coverage"]["ratio"], 1.0)
        self.assertEqual(evidence["harness"]["conclusion_latency_ms"], [3000])
        self.assertEqual(evidence["harness"]["model_usage"]["work_turns"], 1)
        self.assertEqual(evidence["harness"]["model_usage"]["automatic_control_turns"], 1)
        self.assertTrue(evidence["checks"]["work_turn_visible"])

    def test_pending_receipt_without_a_non_blackboard_work_watermark_is_not_visible_work(self):
        evidence = assisted_live.evaluate_observation(
            initial_revision=3,
            task={
                "status": "running",
                "runtime_activity": {"liveness": "live", "turn_activity": "idle"},
                "blackboard_conclusion": {"mode": "assisted", "state": "clean", "applied_revision": 4},
            },
            timeline={"items": [
                {"type": "harness", "content": "Blackboard conclusion pending for work Turn work-1"},
                {"type": "harness", "content": "Blackboard conclusion applied at revision 4"},
            ]},
            events={"events": [{
                "kind": "blackboard_conclusion",
                "payload": {
                    "phase": "pending_detected",
                    "source_work_watermark": 1,
                    "semantic_persistence_watermark": 1,
                },
            }]},
            attempt_detail={"type": "attempt", "record": {"status": "succeeded"}},
        )

        self.assertFalse(evidence["passed"])
        self.assertFalse(evidence["checks"]["work_turn_visible"])

    def test_solved_counter_does_not_count_as_semantic_coverage(self):
        evidence = assisted_live.evaluate_observation(
            initial_revision=3,
            task={
                "status": "running",
                "solved_count": 99,
                "runtime_activity": {"liveness": "live", "turn_activity": "idle"},
                "blackboard_conclusion": {"mode": "assisted", "state": "clean", "applied_revision": 4},
            },
            timeline={"items": []},
            attempt_detail={"type": "attempt", "record": {"status": "completed"}},
        )

        self.assertFalse(evidence["passed"])
        self.assertEqual(evidence["coverage"]["completed_work_turns"], 0)
        self.assertNotIn("solved_count", str(evidence))

    def test_action_required_receipt_is_visible_coverage_but_not_live_success(self):
        evidence = assisted_live.evaluate_observation(
            initial_revision=3,
            task={
                "status": "running",
                "runtime_activity": {"liveness": "live", "turn_activity": "idle"},
                "blackboard_conclusion": {"mode": "assisted", "state": "action_required"},
            },
            timeline={"items": [
                {"seq": 1, "type": "harness", "content": "Blackboard conclusion pending for work Turn work-1"},
                {"seq": 2, "type": "harness", "content": "Blackboard conclusion requires action (invalid_result)"},
            ]},
            attempt_detail=None,
        )

        self.assertFalse(evidence["passed"])
        self.assertEqual(evidence["coverage"]["action_required_receipts"], 1)
        self.assertEqual(evidence["coverage"]["covered_work_turns"], 1)

    def test_automatic_finish_or_objective_dispatch_fails_validation(self):
        evidence = assisted_live.evaluate_observation(
            initial_revision=0,
            task={
                "status": "completed",
                "runtime_activity": {"liveness": "offline"},
                "blackboard_conclusion": {"mode": "assisted", "state": "clean", "applied_revision": 1},
            },
            timeline={"items": [
                {"seq": 1, "type": "harness", "content": "Blackboard conclusion pending for work Turn work-1"},
                {"seq": 2, "type": "harness", "content": "Blackboard conclusion applied at revision 1"},
                {"seq": 3, "type": "lifecycle", "content": "Task Finish completed"},
                {"seq": 4, "type": "harness", "content": "Objective dispatch requested"},
            ]},
            attempt_detail={"type": "attempt", "record": {"status": "completed"}},
        )

        self.assertFalse(evidence["passed"])
        self.assertFalse(evidence["checks"]["no_automatic_task_finish"])
        self.assertFalse(evidence["checks"]["no_automatic_objective_dispatch"])

    def test_evidence_is_allowlisted_and_does_not_echo_sensitive_payloads(self):
        evidence = assisted_live.evaluate_observation(
            initial_revision=0,
            task={
                "status": "running",
                "runtime_activity": {"liveness": "live", "turn_activity": "idle"},
                "blackboard_conclusion": {"mode": "assisted", "state": "action_required", "error_code": "invalid_result"},
                "prompt": "SECRET PROMPT",
                "credential": "SECRET TOKEN",
            },
            timeline={"items": [
                {"seq": 1, "type": "harness", "content": "Blackboard conclusion pending for work Turn work-1", "output": "RAW SECRET"},
                {"seq": 2, "type": "harness", "content": "Blackboard conclusion requires action (invalid_result)", "reasoning": "SECRET REASONING"},
            ]},
            attempt_detail=None,
        )

        encoded = str(evidence)
        for secret in ("SECRET PROMPT", "SECRET TOKEN", "RAW SECRET", "SECRET REASONING"):
            self.assertNotIn(secret, encoded)


if __name__ == "__main__":
    unittest.main()
