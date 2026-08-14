#!/usr/bin/env python3
"""Validate TSecBench local-mode API access without solving a challenge."""

from __future__ import annotations

import json
import os
import secrets
import sys
import urllib.error
import urllib.parse
import urllib.request


TIMEOUT_SECONDS = 15
MAX_RESPONSE_BYTES = 1024 * 1024


class ValidationError(Exception):
    """A safe, phase-specific local-mode validation failure."""


def http_transport(method, url, headers, body, timeout):
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, _bounded_read(response)
    except urllib.error.HTTPError as error:
        return error.code, _bounded_read(error)
    except (urllib.error.URLError, TimeoutError, OSError) as error:
        reason = "timeout" if isinstance(error, TimeoutError) else type(error).__name__
        raise ValidationError(f"transport failed ({reason}); verify the host VPN and host-network routing") from error


def _bounded_read(response):
    payload = response.read(MAX_RESPONSE_BYTES + 1)
    if len(payload) > MAX_RESPONSE_BYTES:
        raise ValidationError("response exceeded the 1 MiB limit")
    return payload


def _request(transport, phase, method, url, token, body=None, on_success_status=None):
    headers = {"BENCHMARK_TOKEN": token, "Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    try:
        status, payload = transport(method, url, headers, body, TIMEOUT_SECONDS)
    except ValidationError as error:
        raise ValidationError(f"{phase} failed: {error}") from error
    except (urllib.error.URLError, TimeoutError, OSError) as error:
        raise ValidationError(
            f"{phase} failed: transport error ({type(error).__name__}); "
            "verify the host VPN and host-network routing"
        ) from error
    except Exception as error:
        raise ValidationError(f"{phase} failed: transport error ({type(error).__name__})") from error
    if not 200 <= status < 300:
        code = _error_code(payload)
        suffix = f" ({code})" if code else ""
        raise ValidationError(f"{phase} failed: HTTP {status}{suffix}")
    if on_success_status is not None:
        on_success_status()
    try:
        return json.loads(payload)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValidationError(f"{phase} failed: malformed JSON response") from error


def _error_code(payload):
    try:
        value = json.loads(payload)
    except (UnicodeDecodeError, json.JSONDecodeError, TypeError):
        return ""
    if isinstance(value, dict) and isinstance(value.get("code"), str):
        code = value["code"]
        if code and all(character.isalnum() or character in "_-" for character in code):
            return code[:80]
    return ""


def _challenge_url(api_base, action=None, unique_code=None):
    url = api_base if action is None else f"{api_base}/{action}"
    if unique_code is not None:
        url += "?" + urllib.parse.urlencode({"unique_code": unique_code})
    return url


def validate(base_url, token, transport=http_transport, probe=None):
    """Run one bounded list, start, invalid submit, and close smoke."""
    parsed = urllib.parse.urlsplit(base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.username or parsed.password:
        raise ValidationError("config failed: BENCHMARK_BASE_URL must be an HTTP(S) origin or path without credentials")
    if parsed.query or parsed.fragment:
        raise ValidationError("config failed: BENCHMARK_BASE_URL must not contain a query or fragment")
    if not token:
        raise ValidationError("config failed: BENCHMARK_TOKEN is empty")

    api_base = base_url.rstrip("/") + "/openapi/v1/challenges"
    challenges = _request(transport, "list", "GET", _challenge_url(api_base), token)
    if not isinstance(challenges, list):
        raise ValidationError("list failed: response must be an array")

    selected = next(
        (
            challenge
            for challenge in challenges
            if isinstance(challenge, dict)
            and challenge.get("is_completed") is False
            and challenge.get("container_status") == "stopped"
            and isinstance(challenge.get("unique_code"), str)
            and challenge["unique_code"]
        ),
        None,
    )
    if selected is None:
        raise ValidationError("selection failed: no incomplete stopped challenge is available")

    code = selected["unique_code"]
    started = False
    primary_error = None

    def mark_started():
        nonlocal started
        started = True

    try:
        _request(
            transport,
            "start",
            "POST",
            _challenge_url(api_base, "start", code),
            token,
            on_success_status=mark_started,
        )
        invalid_probe = probe or "cyberpenda-local-api-smoke-invalid-" + secrets.token_hex(16)
        body = json.dumps({"unique_code": code, "flag": invalid_probe}, separators=(",", ":")).encode("utf-8")
        submitted = _request(transport, "submit", "POST", _challenge_url(api_base, "submit"), token, body)
        if not isinstance(submitted, dict) or submitted.get("correct") is not False or submitted.get("awarded") != 0:
            raise ValidationError("submit failed: the known-invalid probe returned an unexpected result")
    except BaseException as error:
        primary_error = error

    cleanup_error = None
    if started:
        try:
            _request(transport, "close", "POST", _challenge_url(api_base, "close", code), token)
        except BaseException as error:
            cleanup_error = error

    if primary_error is not None:
        if cleanup_error is not None:
            raise ValidationError(f"{primary_error}; cleanup failed: {_cleanup_detail(cleanup_error)}") from primary_error
        raise primary_error
    if cleanup_error is not None:
        raise cleanup_error


def _cleanup_detail(error):
    message = str(error)
    prefix = "close failed: "
    return message[len(prefix):] if message.startswith(prefix) else message


def main():
    try:
        validate(
            os.environ.get("BENCHMARK_BASE_URL", ""),
            os.environ.get("BENCHMARK_TOKEN", ""),
        )
    except KeyboardInterrupt:
        print("local-mode validation interrupted; cleanup was attempted", file=sys.stderr)
        return 130
    except ValidationError as error:
        print(f"local-mode validation failed: {error}", file=sys.stderr)
        return 1
    print("OK: list, start, submit, and close API access validated")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
