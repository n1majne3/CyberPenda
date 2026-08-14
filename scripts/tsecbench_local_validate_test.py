import importlib.util
import json
import pathlib
import unittest
import urllib.error


SCRIPT = pathlib.Path(__file__).parents[1] / "docker" / "tsecbench-hosted" / "tsecbench-local-validate.py"
SPEC = importlib.util.spec_from_file_location("tsecbench_local_validate", SCRIPT)
validator = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(validator)


class FakeTransport:
    def __init__(self, responses):
        self.responses = list(responses)
        self.requests = []

    def __call__(self, method, url, headers, body, timeout):
        self.requests.append((method, url, headers, body, timeout))
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class TSecBenchLocalValidateTest(unittest.TestCase):
    def test_validates_list_start_invalid_submit_and_close(self):
        transport = FakeTransport([
            (200, b'[{"unique_code":"web-1","is_completed":false,"container_status":"stopped"}]'),
            (200, b'{"unique_code":"web-1","container_addr":["10.0.0.2:80"]}'),
            (200, b'{"correct":false,"awarded":0}'),
            (200, b'{"closed":true}'),
        ])

        validator.validate("https://bench.invalid/root/", "one-use-token", transport, probe="known-invalid-probe")

        self.assertEqual([request[0] for request in transport.requests], ["GET", "POST", "POST", "POST"])
        self.assertEqual(
            [request[1] for request in transport.requests],
            [
                "https://bench.invalid/root/openapi/v1/challenges",
                "https://bench.invalid/root/openapi/v1/challenges/start?unique_code=web-1",
                "https://bench.invalid/root/openapi/v1/challenges/submit",
                "https://bench.invalid/root/openapi/v1/challenges/close?unique_code=web-1",
            ],
        )
        for request in transport.requests:
            self.assertEqual(request[2]["BENCHMARK_TOKEN"], "one-use-token")
            self.assertNotIn("one-use-token", request[1])
            self.assertLessEqual(request[4], 15)
        self.assertEqual(
            json.loads(transport.requests[2][3]),
            {"unique_code": "web-1", "flag": "known-invalid-probe"},
        )

    def test_submit_failure_closes_only_the_challenge_started_by_this_run(self):
        transport = FakeTransport([
            (200, b'['
                  b'{"unique_code":"already-active","is_completed":false,"container_status":"available"},'
                  b'{"unique_code":"ours","is_completed":false,"container_status":"stopped"}]'),
            (200, b'{"unique_code":"ours"}'),
            (500, b'{"code":"resource_unavailable","message":"secret detail"}'),
            (200, b'{"closed":true}'),
        ])

        with self.assertRaisesRegex(validator.ValidationError, r"submit failed: HTTP 500 \(resource_unavailable\)"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertEqual(len(transport.requests), 4)
        self.assertIn("unique_code=ours", transport.requests[-1][1])
        self.assertNotIn("already-active", transport.requests[-1][1])

    def test_start_failure_does_not_close_a_challenge(self):
        transport = FakeTransport([
            (200, b'[{"unique_code":"ours","is_completed":false,"container_status":"stopped"}]'),
            (429, b'{"code":"resource_unavailable","message":"try later"}'),
        ])

        with self.assertRaisesRegex(validator.ValidationError, r"start failed: HTTP 429 \(resource_unavailable\)"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertEqual(len(transport.requests), 2)

    def test_malformed_successful_start_response_still_closes_the_started_challenge(self):
        transport = FakeTransport([
            (200, b'[{"unique_code":"ours","is_completed":false,"container_status":"stopped"}]'),
            (200, b'not-json'),
            (200, b'{"closed":true}'),
        ])

        with self.assertRaisesRegex(validator.ValidationError, "start failed: malformed JSON response"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertEqual(len(transport.requests), 3)
        self.assertIn("unique_code=ours", transport.requests[-1][1])

    def test_cleanup_failure_does_not_replace_the_submit_failure(self):
        transport = FakeTransport([
            (200, b'[{"unique_code":"ours","is_completed":false,"container_status":"stopped"}]'),
            (200, b'{}'),
            (500, b'{"code":"submit_failed"}'),
            (500, b'{"code":"close_failed"}'),
        ])

        with self.assertRaisesRegex(validator.ValidationError, r"submit failed: HTTP 500 \(submit_failed\).+cleanup failed: HTTP 500 \(close_failed\)"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

    def test_no_stopped_challenge_is_a_bounded_selection_failure(self):
        transport = FakeTransport([
            (200, b'[{"unique_code":"active","is_completed":false,"container_status":"available"}]'),
        ])

        with self.assertRaisesRegex(validator.ValidationError, "selection failed: no incomplete stopped challenge is available"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertEqual(len(transport.requests), 1)

    def test_malformed_list_response_stops_before_start(self):
        transport = FakeTransport([(200, b'not-json')])

        with self.assertRaisesRegex(validator.ValidationError, "list failed: malformed JSON response"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertEqual(len(transport.requests), 1)

    def test_transport_failure_is_bounded_and_advises_host_vpn(self):
        transport = FakeTransport([urllib.error.URLError("private sensitive transport detail")])

        with self.assertRaisesRegex(
            validator.ValidationError,
            r"list failed: transport error \(URLError\); verify the host VPN and host-network routing",
        ) as failure:
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertNotIn("private sensitive transport detail", str(failure.exception))
        self.assertLessEqual(transport.requests[0][4], 15)

    def test_unexpected_correct_probe_still_closes_its_challenge(self):
        transport = FakeTransport([
            (200, b'[{"unique_code":"ours","is_completed":false,"container_status":"stopped"}]'),
            (200, b'{}'),
            (200, b'{"correct":true,"awarded":1}'),
            (200, b'{"closed":true}'),
        ])

        with self.assertRaisesRegex(validator.ValidationError, "known-invalid probe returned an unexpected result"):
            validator.validate("https://bench.invalid", "token", transport, probe="invalid")

        self.assertEqual(len(transport.requests), 4)

    def test_invalid_base_url_stops_before_transport(self):
        transport = FakeTransport([])

        with self.assertRaisesRegex(validator.ValidationError, "config failed"):
            validator.validate("file:///secret", "token", transport, probe="invalid")

        self.assertEqual(transport.requests, [])


if __name__ == "__main__":
    unittest.main()
