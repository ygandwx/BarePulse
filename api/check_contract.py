#!/usr/bin/env python3
"""Offline structural/Go-route contract checks; Python standard library only.

openapi.yaml deliberately uses JSON (a YAML 1.2 subset). This is not a full
OpenAPI metaschema validator, nor a claim that response examples were executed.
It catches stale routes, renamed JSON fields, broken references, missing path
parameters and accidentally weakened authentication without importing tooling.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
METHODS = {"get", "post", "put", "patch", "delete", "head", "options", "trace"}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def walk(value):
    if isinstance(value, dict):
        yield value
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


def check(spec_path: Path):
    document = json.loads(spec_path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    errors = []

    def require(condition, message):
        if not condition:
            errors.append(message)

    require(document.get("openapi") == "3.1.0", "OpenAPI must be 3.1.0")
    require(bool(document.get("info", {}).get("version")), "info.version missing")
    sources = "\n".join(path.read_text(encoding="utf-8") for path in sorted((ROOT / "backend/internal/app").glob("*.go")) if not path.name.endswith("_test.go"))
    registration = r'(?:a\.route|a\.Mux\.Handle(?:Func)?|register)\(\s*"((?:GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS) /[^"\n]*)"'
    actual = set(re.findall(registration, sources))
    admin_routes = set(re.findall(r'a\.route\(\s*"([A-Z]+ /[^"\n]*)"\s*,\s*true\s*,', sources))
    # The WHMCS router registers a finite verb loop then dispatches by action.
    dynamic = re.findall(r'for\s+_,\s*(\w+)\s*:=\s*range\s*\[\]string\{([^}]+)\}\s*\{\s*a\.Mux\.HandleFunc\(\s*\1\s*\+\s*" (/[^"]+)"', sources)
    for _, verbs, path in dynamic:
        actual.update(verb + " " + path for verb in re.findall(r'"([A-Z]+)"', verbs))
    require(bool(actual), "No Go HTTP registrations found")
    require(len(dynamic) == 1, "Dynamic route registration changed; review extractor and API expansion")

    documented = set()
    operation_ids = set()
    security_schemes = document.get("components", {}).get("securitySchemes", {})
    operations = []
    for path, path_item in document.get("paths", {}).items():
        require(path.startswith("/"), f"Invalid path: {path}")
        for method, operation in path_item.items():
            if method not in METHODS:
                continue
            label = f"{method.upper()} {path}"
            operations.append((path, method, operation))
            registered = operation.get("x-go-route", label)
            documented.add(registered)
            require(registered.startswith(method.upper() + " "), f"Method mismatch: {label}")
            operation_id = operation.get("operationId")
            require(operation_id and operation_id not in operation_ids, f"Missing/duplicate operationId: {label}")
            operation_ids.add(operation_id)
            require(bool(operation.get("responses")), f"Responses missing: {label}")
            require(bool(operation.get("summary")), f"Summary missing: {label}")
            require("security" in operation, f"Explicit authentication missing: {label}")
            public = {"GET /", "GET /healthz", "POST /api/v1/login", "POST /api/v1/portal/redeem"}
            if label in public:
                expected_security = []
            elif "/whmcs/services/" in path:
                expected_security = [{"IntegrationInstance": [], "IntegrationTimestamp": [], "IntegrationNonce": [], "IntegrationSignature": []}]
            else:
                expected_security = [{"PortalToken": []}] if path.startswith("/api/v1/portal/") else [{"SessionCookie": []}]
                if "SessionCookie" in expected_security[0] and method not in {"get", "head", "options"}:
                    expected_security[0]["CSRF"] = []
                if path.endswith(("/events", "/input")):
                    expected_security[0]["ConsoleToken"] = []
            require(operation.get("security") == expected_security, f"Authentication contract weakened or changed: {label}")
            if registered in admin_routes:
                require(operation.get("x-required-role") == "admin", f"Administrator role not documented: {label}")
            params = path_item.get("parameters", []) + operation.get("parameters", [])
            path_params = {p.get("name") for p in params if p.get("in") == "path" and p.get("required") is True}
            require(path_params == set(re.findall(r"\{([^}]+)\}", path)), f"Required path parameters mismatch: {label}")
            for security in operation.get("security", []):
                require(isinstance(security, dict), f"Invalid security requirement: {label}")
                for scheme, scopes in security.items():
                    require(scheme in security_schemes, f"Unknown security scheme {scheme}: {label}")
                    require(scopes == [], f"API-key scopes must be empty: {label}")
                if "SessionCookie" in security and method not in {"get", "head", "options"}:
                    require("CSRF" in security, f"Unsafe session endpoint missing CSRF: {label}")
                if "/whmcs/services/" in path:
                    require(set(security) == {"IntegrationInstance", "IntegrationTimestamp", "IntegrationNonce", "IntegrationSignature"}, f"HMAC headers must be combined (AND): {label}")
                if path.startswith("/api/v1/portal/") and path != "/api/v1/portal/redeem":
                    require("PortalToken" in security, f"Portal scope missing: {label}")
            if path.endswith(("/events", "/input")):
                require(all("ConsoleToken" in sec for sec in operation.get("security", [])), f"Live channel access token missing: {label}")
            for code, response in operation.get("responses", {}).items():
                require(code == "default" or re.fullmatch(r"[1-5][0-9X]{2}", code), f"Invalid response status {code}: {label}")
                require("$ref" in response or bool(response.get("description")), f"Response description missing: {label}/{code}")

    require(actual == documented, "Route coverage mismatch:\n  Undocumented: " + repr(sorted(actual - documented)) + "\n  Not registered: " + repr(sorted(documented - actual)))
    expected_actions = {"get": {"summary", "power", "traffic", "task"}, "post": {"power-actions", "console", "lifecycle"}}
    for method, actions in expected_actions.items():
        described_actions = {path.rsplit("/", 1)[-1] for path, verb, _ in operations if "/whmcs/services/" in path and verb == method}
        require(actions == described_actions, f"WHMCS {method} expansion incomplete")
    implemented_actions = set(re.findall(r'case "([\w-]+)":', (ROOT / "backend/internal/app/whmcs.go").read_text(encoding="utf-8")))
    # lifecycle is intentionally dispatched before the GET/POST action switch.
    require((expected_actions["get"] | expected_actions["post"]) - {"lifecycle"} == implemented_actions, "WHMCS action dispatch drift")

    for node in walk(document):
        if "$ref" in node:
            pointer = node["$ref"]
            require(pointer.startswith("#/"), f"Nonlocal reference cannot be verified offline: {pointer}")
            if pointer.startswith("#/"):
                target = document
                try:
                    for key in pointer[2:].split("/"):
                        target = target[key.replace("~1", "/").replace("~0", "~")]
                except (KeyError, TypeError):
                    errors.append(f"Unresolved reference: {pointer}")
        if "required" in node and "properties" in node:
            require(isinstance(node["required"], list), "Schema required must be an array")
            # Conditional/allOf schemas can require fields defined by their parent.
            if node.get("additionalProperties") is False:
                require(set(node["required"]) <= set(node["properties"]), "Required fields excluded by closed object schema")
        if "x-go-type" in node:
            go_type = node["x-go-type"].split(".")[-1]
            match = re.search(r"type\s+" + re.escape(go_type) + r"\s+struct\s*\{(.*?)\n\}", sources, re.S)
            require(match is not None, f"Go type missing: {go_type}")
            if match:
                tags = set(re.findall(r'json:"([^",]+)(?:,[^"]*)?"', match.group(1))) - {"-"}
                require(tags == set(node.get("properties", {})), f"Go JSON fields differ: {go_type}: code-only={sorted(tags - set(node.get('properties', {})))} spec-only={sorted(set(node.get('properties', {})) - tags)}")

    acceptance = (ROOT / "docs/offline-acceptance.md").read_text(encoding="utf-8")
    # Exact inline names only: command-line -run expressions may be prefixes.
    cited_tests = set(re.findall(r"`(Test[A-Z][A-Za-z0-9_]+)`", acceptance))
    test_sources = "\n".join(path.read_text(encoding="utf-8") for path in (ROOT / "backend").rglob("*_test.go"))
    available_tests = set(re.findall(r"func\s+(Test\w+)\s*\(", test_sources))
    require(cited_tests <= available_tests, f"Acceptance matrix cites missing tests: {sorted(cited_tests - available_tests)}")

    if errors:
        raise ValueError("\n".join(errors))
    print(f"PASS: OpenAPI JSON structure, {len(operations)} operations, {len(actual)} Go registrations, local references, authentication requirements, Go JSON fields and {len(cited_tests)} acceptance test references")


if __name__ == "__main__":
    try:
        check(Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT / "api/openapi.yaml")
    except (OSError, ValueError, TypeError) as error:
        print(f"Contract check failed: {error}", file=sys.stderr)
        sys.exit(1)
