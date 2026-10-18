#!/usr/bin/env python3
"""Export a clean, committed HEAD without reading ignored workspace contents.

Only Python's standard library and local Git are required. This command does not
run application tests, fetch remote refs, publish anything, or back up live data.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tempfile
import unicodedata
import zipfile


PREFIX = "BarePulse/"
SOURCE_NAME = "BarePulse-source.zip"
BUNDLE_NAME = "BarePulse-checkpoint.bundle"
FULL_OID = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")
WINDOWS_DEVICE = re.compile(r"(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?\Z", re.I)


class ExportError(Exception):
    pass


class Git:
    def __init__(self, directory, timeout=300, line_endings=None):
        self.directory = Path(directory)
        self.timeout = timeout
        self.line_endings = line_endings or {}
        # Inherited GIT_DIR/INDEX_FILE/OBJECT_DIRECTORY must not redirect the
        # snapshot. No lazy object download, remote protocol, prompt or hook.
        self.env = {k: v for k, v in os.environ.items() if not k.upper().startswith("GIT_")}
        self.env.update({
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0", "GIT_NO_LAZY_FETCH": "1",
            "GIT_OPTIONAL_LOCKS": "0", "GIT_GRAFT_FILE": os.devnull,
        })

    def run(self, *args, allow_failure=False):
        command = ["git", "--no-replace-objects", "-c", "protocol.allow=never",
                   "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.devnull,
                   "-c", "core.attributesFile=" + os.devnull]
        for key, value in self.line_endings.items():
            command.extend(["-c", key + "=" + value])
        if args and args[0] == "archive":
            # Archive committed bytes, not the platform checkout conversion.
            # Blob-identity verification still rejects changing attributes.
            command.extend(["-c", "core.autocrlf=false", "-c", "core.eol=lf"])
        command.extend(["-C", str(self.directory), *args])
        try:
            result = subprocess.run(command, env=self.env, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, timeout=self.timeout, check=False)
        except FileNotFoundError as exc:
            raise ExportError("Git executable or repository directory is unavailable") from exc
        except subprocess.TimeoutExpired as exc:
            raise ExportError("Local Git command timed out: " + args[0]) from exc
        if result.returncode and not allow_failure:
            # Do not copy arbitrary repository configuration or command stderr
            # into a public handoff manifest.
            raise ExportError(f"Local git {args[0]} failed (exit {result.returncode})")
        return result

    def text(self, *args):
        try:
            return self.run(*args).stdout.decode("utf-8").rstrip("\r\n")
        except UnicodeDecodeError as exc:
            raise ExportError("Repository paths and refs must use UTF-8") from exc

    def effective_line_endings(self):
        # Read only these effective values, including standard system/global
        # files and repository overrides. `git config --get` does not invoke
        # hooks, filters or credential helpers. Inherited GIT_* variables remain
        # stripped, and every later operation still disables system/global config.
        environment = self.env.copy()
        environment.pop("GIT_CONFIG_NOSYSTEM")
        environment.pop("GIT_CONFIG_GLOBAL")
        allowed = {
            "core.autocrlf": {"true": "true", "yes": "true", "on": "true", "1": "true", "": "true",
                              "false": "false", "no": "false", "off": "false", "0": "false", "input": "input"},
            "core.eol": {"lf": "lf", "crlf": "crlf", "native": "native"},
        }
        settings = {}
        for key, values in allowed.items():
            try:
                result = subprocess.run(["git", "--no-replace-objects", "-C", str(self.directory),
                                         "config", "--includes", "--get", key], env=environment,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=self.timeout)
            except (OSError, subprocess.TimeoutExpired) as exc:
                raise ExportError("Cannot read effective Git line-ending configuration") from exc
            if result.returncode == 1:
                continue
            if result.returncode:
                raise ExportError("Cannot read effective Git line-ending configuration: " + key)
            value = result.stdout.decode("ascii", errors="replace").strip().lower()
            if value not in values:
                raise ExportError("Unsupported effective Git line-ending value for " + key)
            settings[key] = values[value]
        return settings


def is_within(path, directory):
    try:
        path.relative_to(directory)
        return True
    except ValueError:
        return False


def output_path(value, repo, git):
    raw = Path(value).expanduser()
    if ".." in raw.parts:
        raise ExportError("Output must not contain '..' traversal components")
    if os.path.lexists(raw):
        raise ExportError("Output already exists; choose a new directory")
    try:
        parent = raw.absolute().parent.resolve(strict=True)
    except OSError as exc:
        raise ExportError("Output parent must already exist") from exc
    if not parent.is_dir():
        raise ExportError("Output parent must be a directory")
    target = parent / raw.name
    protected = [repo]
    for option in ("--git-dir", "--git-common-dir"):
        protected.append(Path(git.text("rev-parse", "--path-format=absolute", option)).resolve())
    if any(is_within(target, directory) for directory in protected):
        raise ExportError("Output must be outside the worktree and Git metadata directories")
    return target


def clean_snapshot(git):
    if git.text("rev-parse", "--is-bare-repository") != "false":
        raise ExportError("A non-bare working repository is required")
    if git.text("rev-parse", "--is-shallow-repository") != "false":
        raise ExportError("Shallow history cannot produce a self-contained checkpoint bundle")
    if git.run("status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none").stdout:
        raise ExportError("Dirty worktree: commit tracked changes and untracked, non-ignored files first")
    # status intentionally honors these bits; exporting under them would make
    # the clean-worktree claim misleading. Do not silently clear user flags.
    for entry in git.run("ls-files", "-v", "-z").stdout.split(b"\0"):
        if entry and (entry[:1] == b"S" or entry[:1].islower()):
            raise ExportError("Assume-unchanged or skip-worktree index entries are unsupported")
    head = git.text("rev-parse", "--verify", "HEAD^{commit}")
    tree = git.text("rev-parse", "--verify", head + "^{tree}")
    branch_result = git.run("symbolic-ref", "--quiet", "--short", "HEAD", allow_failure=True)
    if branch_result.returncode not in (0, 1):
        raise ExportError("Cannot determine the current branch")
    branch = branch_result.stdout.decode("utf-8").rstrip("\r\n") or None
    return {"branch": branch, "local_head": head, "tree": tree}


def safe_source_path(name):
    path = PurePosixPath(name)
    if not name or name.startswith("/") or "\\" in name or path.as_posix() != name:
        raise ExportError("Source contains an unsafe or non-canonical archive path")
    for part in path.parts:
        if part in (".", "..") or part.lower() == ".git" or part.endswith((" ", ".")):
            raise ExportError("Source contains an unsafe archive path component")
        if any(ord(c) < 32 or ord(c) == 127 or c in ':<>"|?*' for c in part) or WINDOWS_DEVICE.fullmatch(part):
            raise ExportError("Source path is not safe for portable extraction")


def tracked_files(git, head):
    files = {}
    folded = set()
    for record in git.run("ls-tree", "-r", "-z", "--full-tree", head).stdout.split(b"\0"):
        if not record:
            continue
        metadata, raw_name = record.split(b"\t", 1)
        mode, kind, oid = metadata.decode("ascii").split(" ")
        try:
            name = raw_name.decode("utf-8")
        except UnicodeDecodeError as exc:
            raise ExportError("Tracked paths must use UTF-8") from exc
        safe_source_path(name)
        if mode not in ("100644", "100755") or kind != "blob":
            raise ExportError("Symlinks, submodules and non-regular tracked entries are unsupported")
        key = unicodedata.normalize("NFC", name).casefold()
        if key in folded:
            raise ExportError("Case/Unicode-colliding tracked paths are not portable")
        folded.add(key)
        files[name] = {"git_blob": oid, "git_mode": mode}
    for name in folded:
        if any(str(parent) in folded for parent in PurePosixPath(name).parents if str(parent) != "."):
            raise ExportError("Tracked file/directory paths collide on a case-insensitive filesystem")
    if not files:
        raise ExportError("HEAD contains no regular tracked files")
    return files


def verify_archive(path, expected, object_format):
    manifest = []
    allowed_dirs = {PREFIX}
    for name in expected:
        allowed_dirs.update(PREFIX + str(parent) + "/" for parent in PurePosixPath(name).parents if str(parent) != ".")
    try:
        with zipfile.ZipFile(path) as archive:
            entries = archive.infolist()
            names = [item.filename for item in entries]
            if len(names) != len(set(names)):
                raise ExportError("Archive contains duplicate entry names")
            actual = set()
            for item in entries:
                if item.is_dir():
                    if item.filename not in allowed_dirs:
                        raise ExportError("Archive contains an unexpected directory")
                    continue
                if not item.filename.startswith(PREFIX):
                    raise ExportError("Archive entry is outside the fixed source prefix")
                name = item.filename[len(PREFIX):]
                safe_source_path(name)
                if name not in expected:
                    raise ExportError("Archive contains a file not tracked by the selected HEAD")
                actual.add(name)
                sha256 = hashlib.sha256()
                git_hash = hashlib.new(object_format)
                git_hash.update(f"blob {item.file_size}\0".encode("ascii"))
                size = 0
                with archive.open(item) as stream:
                    while chunk := stream.read(1024 * 1024):
                        size += len(chunk)
                        sha256.update(chunk)
                        git_hash.update(chunk)
                if size != item.file_size or git_hash.hexdigest() != expected[name]["git_blob"]:
                    raise ExportError("Archive content differs from the committed Git blob (check export-subst attributes)")
                manifest.append({"path": name, "bytes": size, "sha256": sha256.hexdigest(), **expected[name]})
            if actual != set(expected):
                raise ExportError("Archive file list differs from committed HEAD (check export-ignore attributes)")
            if archive.testzip() is not None:
                raise ExportError("Archive CRC integrity check failed")
    except (zipfile.BadZipFile, RuntimeError, OSError) as exc:
        raise ExportError("Source ZIP is unreadable or corrupt") from exc
    return sorted(manifest, key=lambda entry: entry["path"])


def verify_bundle(path, snapshot, object_format, timeout):
    # Verification in the source repository alone could conceal prerequisites.
    # An empty temporary object database proves this bundle is self-contained.
    with tempfile.TemporaryDirectory(prefix="barepulse-bundle-verify-") as temporary:
        root = Path(temporary)
        template = root / "empty-template"
        template.mkdir()
        repo = root / "verify.git"
        Git(root, timeout).run("init", "--bare", "--object-format=" + object_format,
                               "--template=" + str(template), str(repo))
        git = Git(repo, timeout)
        git.run("bundle", "verify", str(path))
        heads = git.text("bundle", "list-heads", str(path)).splitlines()
        if heads != [snapshot["local_head"] + " HEAD"]:
            raise ExportError("Bundle HEAD differs from the selected checkpoint")
        git.run("bundle", "unbundle", str(path))
        git.run("update-ref", "refs/heads/offline-export", snapshot["local_head"])
        git.run("fsck", "--full", "--strict", "--no-reflogs")
        tree = git.text("rev-parse", "refs/heads/offline-export^{tree}")
        if tree != snapshot["tree"]:
            raise ExportError("Bundle tree differs from the source archive checkpoint")


def load_evidence(path, head):
    result = {"verification": "NOT_PERFORMED_BY_EXPORTER", "checks": []}
    if path is None:
        return result
    try:
        with Path(path).open("rb") as stream:
            data = stream.read(1024 * 1024 + 1)
        if len(data) > 1024 * 1024:
            raise ExportError("Evidence JSON exceeds 1 MiB")
        payload = json.loads(data.decode("utf-8"))
    except (OSError, UnicodeError, ValueError) as exc:
        raise ExportError("Evidence must be a readable UTF-8 JSON document") from exc
    if not isinstance(payload, dict) or set(payload) != {"checks"} or not isinstance(payload["checks"], list) or len(payload["checks"]) > 100:
        raise ExportError("Evidence must contain only a checks array (at most 100 entries)")
    required = {"name", "reported_status", "scope", "evidence"}
    allowed = required | {"tested_commit"}
    for check in payload["checks"]:
        if not isinstance(check, dict) or not required <= check.keys() or not check.keys() <= allowed:
            raise ExportError("Evidence check requires name, reported_status, scope, evidence; only tested_commit is optional")
        if any(not isinstance(value, str) or not value.strip() or len(value) > 2000 for value in check.values()):
            raise ExportError("Evidence values must be non-empty strings of at most 2000 characters")
        if check["reported_status"] not in {"PASS", "FAIL", "NOT_RUN", "BLOCKED"}:
            raise ExportError("Unknown reported evidence status")
        if "tested_commit" in check and not FULL_OID.fullmatch(check["tested_commit"]):
            raise ExportError("tested_commit must be a full lowercase Git commit ID")
        result["checks"].append({**check, "matches_exported_head": check.get("tested_commit") == head})
    result["input_sha256"] = hashlib.sha256(data).hexdigest()
    return result


def digest(path):
    sha256 = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            sha256.update(chunk)
    return {"file": path.name, "bytes": path.stat().st_size, "sha256": sha256.hexdigest()}


def export_package(repo, output, expected_head=None, remote_ref=None, evidence=None, timeout=300):
    try:
        requested = Path(repo).expanduser().resolve(strict=True)
    except OSError as exc:
        raise ExportError("Repository path does not exist") from exc
    git = Git(requested, timeout)
    root = Path(git.text("rev-parse", "--show-toplevel")).resolve(strict=True)
    git = Git(root, timeout)
    git.line_endings = git.effective_line_endings()
    snapshot = clean_snapshot(git)
    if expected_head is not None and (not FULL_OID.fullmatch(expected_head) or expected_head != snapshot["local_head"]):
        raise ExportError("--expected-head must equal the full current HEAD commit ID; historical exports require checking out that commit first")
    target = output_path(output, root, git)
    expected = tracked_files(git, snapshot["local_head"])
    object_format = git.text("rev-parse", "--show-object-format")
    if object_format not in {"sha1", "sha256"}:
        raise ExportError("Unsupported Git object hash format")
    reported = load_evidence(evidence, snapshot["local_head"])
    remote = {"status": "NOT_REQUESTED", "network_accessed": False}
    if remote_ref is not None:
        if not remote_ref.startswith("refs/remotes/") or git.run("check-ref-format", remote_ref, allow_failure=True).returncode:
            raise ExportError("--remote-ref must be an exact local refs/remotes/... reference")
        remote_head = git.text("show-ref", "--verify", "--hash", remote_ref)
        remote_tree = git.text("rev-parse", "--verify", remote_head + "^{tree}")
        if remote_tree != snapshot["tree"]:
            raise ExportError("Locally stored remote ref has a different tree")
        remote = {"status": "SAME_TREE", "ref": remote_ref, "commit": remote_head,
                  "tree": remote_tree, "network_accessed": False,
                  "scope": "Local remote-tracking ref only; remote freshness was not checked"}
    try:
        target.mkdir(mode=0o700)
    except OSError as exc:
        raise ExportError("Cannot exclusively create a new output directory") from exc
    incomplete = target / "EXPORT-INCOMPLETE"
    try:
        incomplete.write_text("Export has not passed artifact verification.\n", encoding="utf-8")
        source, bundle = target / SOURCE_NAME, target / BUNDLE_NAME
        git.run("archive", "--format=zip", "--prefix=" + PREFIX, "--output=" + str(source), snapshot["local_head"])
        file_manifest = verify_archive(source, expected, object_format)
        git.run("bundle", "create", str(bundle), "HEAD")
        verify_bundle(bundle, snapshot, object_format, timeout)
        if clean_snapshot(git) != snapshot:
            raise ExportError("Repository HEAD, branch or worktree changed during export")
        if remote_ref is not None and git.text("show-ref", "--verify", "--hash", remote_ref) != remote["commit"]:
            raise ExportError("Remote-tracking ref changed during export")
        manifest = {
            "format_version": 1, "created_at_utc": datetime.now(timezone.utc).isoformat(),
            "snapshot": {**snapshot, "object_format": object_format, "worktree_clean_at_final_check": True,
                         "worktree_line_endings": git.line_endings},
            "remote_comparison": remote, "test_evidence": reported,
            "artifact_verification": {
                "status": "PASS", "source": "Exact HEAD file set, blob identity, SHA256 and ZIP CRC checked",
                "bundle": "Verified and unbundled into an empty local repository; full strict fsck and commit/tree checked",
                "application_tests_run": False,
            },
            "artifacts": [digest(source), digest(bundle)], "files": file_manifest,
            "boundaries": [
                "Source comes only from committed HEAD; ignored and untracked workspace contents are not copied.",
                "Bundle contains all history reachable from HEAD, not unrelated refs, remote configuration or working files.",
                "This is not a secret scanner: any secrets already committed in reachable history must be resolved before delivery.",
                "No device access, database backup, production deployment or remote publication was performed.",
            ],
        }
        (target / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        incomplete.unlink()
        return manifest
    except (ExportError, OSError, ValueError) as exc:
        # Preserve only this newly created directory as inspectable failed output.
        # Never remove or replace a directory supplied by the caller.
        try:
            (target / "export-failure.json").write_text(json.dumps({"status": "FAIL", "error": str(exc)}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        except OSError:
            pass
        raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=".", help="Working repository or a directory inside it (default: current directory)")
    parser.add_argument("--output", required=True, help="New directory outside the worktree; parent must already exist")
    parser.add_argument("--expected-head", help="Require this full current HEAD SHA; does not select another revision")
    parser.add_argument("--remote-ref", help="Compare an existing local refs/remotes/... tree; never contacts the remote")
    parser.add_argument("--evidence", help="Explicit redacted JSON test reports; not independently verified or executed")
    parser.add_argument("--timeout-seconds", type=int, default=300, help="Per local Git command timeout, 30–3600 (default: 300)")
    args = parser.parse_args(argv)
    if not 30 <= args.timeout_seconds <= 3600:
        parser.error("--timeout-seconds must be between 30 and 3600")
    try:
        manifest = export_package(args.repo, args.output, args.expected_head, args.remote_ref, args.evidence, args.timeout_seconds)
    except (ExportError, OSError, ValueError) as exc:
        print("Export refused/failed: " + str(exc), file=sys.stderr)
        return 1
    print(json.dumps({"output": str(Path(args.output).resolve()), "local_head": manifest["snapshot"]["local_head"],
                      "files": len(manifest["files"]), "artifact_verification": "PASS",
                      "application_tests_run": False}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
