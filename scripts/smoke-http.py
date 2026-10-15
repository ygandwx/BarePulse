#!/usr/bin/env python3
"""Exercise a local BarePulse server using only newly created CI mock assets.

Requires ADMIN_EMAIL and ADMIN_PASSWORD environment variables. SMOKE_BASE_URL
defaults to http://127.0.0.1:18080. Leaves explicitly named CI assets and audit
history in an existing development database; compose-smoke.sh owns its own DB.
No device addresses, console sessions, or pre-existing assets are modified.
"""

import http.cookiejar
import ipaddress
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class SmokeFailure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise SmokeFailure(message)


def local_origin(value):
    try:
        parsed = urllib.parse.urlsplit(value)
        address = ipaddress.ip_address(parsed.hostname or "")
        port = parsed.port
    except ValueError:
        raise SmokeFailure("SMOKE_BASE_URL must use a literal loopback IP and valid port") from None
    require(
        parsed.scheme in ("http", "https")
        and address.is_loopback
        and not parsed.username
        and not parsed.password
        and parsed.path in ("", "/")
        and not parsed.query
        and not parsed.fragment
        and port is not None,
        "SMOKE_BASE_URL must be an explicit loopback origin without credentials or path",
    )
    return value.rstrip("/")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        raise SmokeFailure("HTTP redirects are forbidden in the offline smoke test")


class Client:
    def __init__(self, origin):
        self.origin = origin
        self.csrf = ""
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            NoRedirect(),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
        )

    def request(self, method, path, body=None, expected=200, extra_headers=None):
        require(path.startswith("/") and not path.startswith("//"), "Invalid API path")
        headers = {"Accept": "application/json", "Origin": self.origin}
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        if extra_headers:
            headers.update(extra_headers)
        request = urllib.request.Request(self.origin + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=5) as response:
                require(response.status == expected, f"{method} {path}: unexpected HTTP status {response.status}")
                encoded = response.read(2 * 1024 * 1024 + 1)
                require(len(encoded) <= 2 * 1024 * 1024, "API response exceeded smoke size limit")
                return json.loads(encoded) if encoded else None
        except urllib.error.HTTPError as error:
            # Never dump response bodies, credential payloads, cookies, or tokens.
            raise SmokeFailure(f"{method} {path}: HTTP {error.code}") from None
        except (urllib.error.URLError, TimeoutError, OSError):
            raise SmokeFailure(f"{method} {path}: local HTTP connection failed") from None
        except (UnicodeError, json.JSONDecodeError):
            raise SmokeFailure(f"{method} {path}: invalid JSON response") from None


def await_health(client):
    deadline = time.monotonic() + 90
    while True:
        try:
            health = client.request("GET", "/healthz")
        except SmokeFailure:
            if time.monotonic() >= deadline:
                raise SmokeFailure("Health endpoint did not become available within 90 seconds") from None
            time.sleep(1)
            continue
        require(isinstance(health, dict) and health.get("status") == "ok", "Health status is not ok")
        require(
            health.get("real_devices") is False and health.get("device_writes") is False,
            "Refusing smoke mutations: real device access and writes must both be disabled",
        )
        return


def smoke(client, email, password):
    await_health(client)
    login = client.request("POST", "/api/v1/login", {"email": email, "password": password})
    require(login.get("user", {}).get("role") == "admin", "Smoke requires a local administrator")
    client.csrf = login.get("csrf", "")
    require(bool(client.csrf), "Login did not return a CSRF token")
    suffix = secrets.token_hex(8)
    try:
        server = client.request(
            "POST", "/api/v1/servers",
            {"name": f"CI-SMOKE-{suffix}", "owner_id": login["user"]["id"],
             "model": "Offline CI mock fixture", "notes": "Created by scripts/smoke-http.py; no physical hardware"},
            expected=201,
        )
        device = client.request(
            "POST", "/api/v1/devices",
            {"name": f"CI-MOCK-BMC-{suffix}", "kind": "mock_bmc", "scenario": "success", "enabled": True},
            expected=201,
        )
        require(device.get("kind") == "mock_bmc" and not device.get("address"), "Expected an addressless mock device")
        binding = client.request(
            "POST", "/api/v1/bindings",
            {"server_id": server["id"], "device_id": device["id"], "kind": "power", "port": 1},
            expected=201,
        )
        require(binding.get("active") is True and binding.get("port") == 1, "Power binding was not activated")
        power_path = f"/api/v1/servers/{server['id']}/power-actions"
        key = {"Idempotency-Key": f"ci-smoke-{suffix}"}
        task = client.request("POST", power_path, {"action": "power_on"}, expected=202, extra_headers=key)
        replay = client.request("POST", power_path, {"action": "power_on"}, extra_headers=key)
        require(replay.get("id") == task.get("id"), "Identical idempotency key created a second task")
        deadline = time.monotonic() + 45
        while True:
            task = client.request("GET", f"/api/v1/tasks/{task['id']}")
            if task.get("status") not in ("queued", "running", "verifying"):
                break
            require(time.monotonic() < deadline, "Power task did not complete within 45 seconds")
            time.sleep(0.25)
        require(task.get("status") == "succeeded", "Simulated power task did not succeed")
        result = task.get("result", {})
        require(
            all(result.get(field) is True for field in ("accepted", "completed", "target_reached", "simulated"))
            and result.get("power_state") == "on",
            "Power result lacks verified simulated completion",
        )
        tasks = client.request("GET", f"/api/v1/tasks?server_id={server['id']}")
        require(len(tasks) == 1 and tasks[0].get("id") == task["id"], "New CI server must contain exactly one power task")
        summary = client.request("GET", f"/api/v1/servers/{server['id']}/power")
        require(summary.get("simulated") is True and summary.get("power_state") == "on", "Power summary does not match completed mock task")
        audit = client.request("GET", "/api/v1/audit")
        task_actions = [event.get("action") for event in audit if event.get("object_id") == task["id"]]
        for action in ("power.queued", "power.started", "power.succeeded"):
            require(task_actions.count(action) == 1, f"Expected exactly one {action} audit event")
        return {
            "result": "PASS", "base_url": client.origin, "device_access": "disabled",
            "server_id": server["id"], "device_id": device["id"], "task_id": task["id"],
            "checks": ["login", "server", "mock_device", "binding", "power_completion", "idempotency", "power_summary", "audit"],
        }
    finally:
        client.request("POST", "/api/v1/logout", {})


def main():
    try:
        origin = local_origin(os.environ.get("SMOKE_BASE_URL", "http://127.0.0.1:18080"))
        email = os.environ.get("ADMIN_EMAIL", "")
        password = os.environ.get("ADMIN_PASSWORD", "")
        require(bool(email) and bool(password), "Set ADMIN_EMAIL and ADMIN_PASSWORD without printing them")
        report = smoke(Client(origin), email, password)
        print(json.dumps(report, separators=(",", ":")))
        return 0
    except SmokeFailure as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    except (KeyError, TypeError, AttributeError):
        print("FAIL: API response did not match the expected contract", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
