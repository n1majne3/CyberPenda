import importlib.util
import subprocess
import unittest
from pathlib import Path
from unittest import mock

SCRIPT_PATH = Path(__file__).resolve().parent / "tsecbench-launch.py"
SPEC = importlib.util.spec_from_file_location("tsecbench_launch", SCRIPT_PATH)
LAUNCH = importlib.util.module_from_spec(SPEC)
assert SPEC is not None and SPEC.loader is not None
SPEC.loader.exec_module(LAUNCH)


class TSecBenchLaunchAuthTest(unittest.TestCase):
    def test_curl_sends_bearer_token_via_stdin_config_not_argv(self) -> None:
        captured = {}

        def fake_run(cmd, input=None, capture_output=True, timeout=None):
            captured["cmd"] = cmd
            captured["input"] = input
            return subprocess.CompletedProcess(cmd, 0, b'{"profiles":[]}\n200', b"")

        with (
            mock.patch.object(LAUNCH, "AUTH_TOKEN", "op-secret-token"),
            mock.patch.object(subprocess, "run", side_effect=fake_run),
        ):
            status, body = LAUNCH._curl(["-X", "GET", "http://127.0.0.1:8787/api/runtime-profiles"])

        self.assertEqual(status, "200")
        self.assertEqual(body, '{"profiles":[]}')
        self.assertIn("-K", captured["cmd"])
        self.assertNotIn("op-secret-token", " ".join(captured["cmd"]))
        self.assertEqual(
            captured["input"],
            b'header = "Authorization: Bearer op-secret-token"\n',
        )

    def test_curl_omits_config_flag_without_auth_token(self) -> None:
        captured = {}

        def fake_run(cmd, input=None, capture_output=True, timeout=None):
            captured["cmd"] = cmd
            captured["input"] = input
            return subprocess.CompletedProcess(cmd, 0, b"{}\n200", b"")

        with (
            mock.patch.object(LAUNCH, "AUTH_TOKEN", ""),
            mock.patch.object(subprocess, "run", side_effect=fake_run),
        ):
            status, _ = LAUNCH._curl(["-X", "GET", "http://127.0.0.1:8787/health"])

        self.assertEqual(status, "200")
        self.assertNotIn("-K", captured["cmd"])
        self.assertIsNone(captured["input"])

    def test_create_session_uses_curl_with_auth_token(self) -> None:
        captured = {}

        def fake_run(cmd, input=None, capture_output=True, timeout=None):
            captured["cmd"] = cmd
            captured["input"] = input
            return subprocess.CompletedProcess(cmd, 0, b'{"id":"sess-1"}\n201', b"")

        with (
            mock.patch.object(LAUNCH, "AUTH_TOKEN", "op-token-2"),
            mock.patch.object(subprocess, "run", side_effect=fake_run),
        ):
            session_id = LAUNCH.create_session(
                "http://127.0.0.1:8787",
                "prof-1",
                "goal text",
                "/tmp/task.ovpn",
                False,
            )

        self.assertEqual(session_id, "sess-1")
        self.assertEqual(
            captured["input"],
            b'header = "Authorization: Bearer op-token-2"\n',
        )

    def test_401_without_token_surfaces_auth_hint(self) -> None:
        with mock.patch.object(LAUNCH, "AUTH_TOKEN", ""):
            self.assertIn("PENTEST_AUTH_TOKEN", LAUNCH._auth_hint("401"))
            self.assertEqual(LAUNCH._auth_hint("500"), "")


if __name__ == "__main__":
    unittest.main()
