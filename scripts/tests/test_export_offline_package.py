"""Actual local Git/ZIP/bundle contracts; every mutation is in a temporary fixture."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile


SCRIPT = Path(__file__).resolve().parents[1] / "export-offline-package.py"
spec = importlib.util.spec_from_file_location("offline_export", SCRIPT)
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


class OfflineExportTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="barepulse-export-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.repo = self.root / "repository with spaces"
        self.repo.mkdir()
        self.git("init", "--initial-branch=codex/delivery")
        self.git("config", "user.name", "Offline Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "core.autocrlf", "false")
        self.git("config", "commit.gpgsign", "false")
        (self.repo / ".gitignore").write_bytes(b".env\nmedia/\ndatabase/\ntoolchain/\nignored/\n")
        (self.repo / "tracked.txt").write_bytes(b"Committed source\n")
        nested = self.repo / "source with spaces"
        nested.mkdir()
        (nested / "unicode-服务器.txt").write_bytes("Exact UTF-8 字节\n".encode())
        self.git("add", ".")
        self.git("commit", "-m", "Initial offline fixture")
        self.first = self.git("rev-parse", "HEAD").strip()
        (self.repo / "tracked.txt").write_bytes(b"Current committed source\n")
        self.git("commit", "-am", "Second offline fixture")
        self.head = self.git("rev-parse", "HEAD").strip()
        self.tree = self.git("rev-parse", "HEAD^{tree}").strip()
        self.secret = b"PRIVATE-IGNORED-FIXTURE-DO-NOT-EXPORT-78b1976d"
        for name in (".env", "media/private.iso", "database/private.dump", "toolchain/private.exe"):
            path = self.repo / name
            path.parent.mkdir(exist_ok=True)
            path.write_bytes(self.secret)
        self.output = self.root / "delivery with spaces"

    def git(self, *args, repo=None):
        env = {key: value for key, value in os.environ.items() if not key.upper().startswith("GIT_")}
        env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull, "GIT_TERMINAL_PROMPT": "0"})
        result = subprocess.run(["git", "-C", str(repo or self.repo), *args], env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
        if result.returncode:
            self.fail(f"Fixture git {args[0]} failed: {result.stderr.decode(errors='replace')}")
        return result.stdout.decode("utf-8")

    def invoke(self, *args, success=True, repo=None, output=None, env=None):
        result = subprocess.run([sys.executable, str(SCRIPT), "--repo", str(repo or self.repo),
                                 "--output", str(output or self.output), *args],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90, env=env)
        message = result.stderr.decode("utf-8", errors="replace")
        if success:
            self.assertEqual(result.returncode, 0, message)
            return json.loads((Path(output or self.output) / "manifest.json").read_text(encoding="utf-8"))
        self.assertNotEqual(result.returncode, 0, result.stdout.decode(errors="replace"))
        return message

    def test_real_archive_bundle_hashes_and_ignored_secrets(self):
        manifest = self.invoke("--expected-head", self.head)
        self.assertEqual(manifest["snapshot"]["branch"], "codex/delivery")
        self.assertEqual(manifest["snapshot"]["local_head"], self.head)
        self.assertEqual(manifest["snapshot"]["tree"], self.tree)
        self.assertEqual(manifest["test_evidence"]["checks"], [])
        self.assertFalse(manifest["artifact_verification"]["application_tests_run"])
        self.assertFalse((self.output / "EXPORT-INCOMPLETE").exists())
        expected = {".gitignore", "tracked.txt", "source with spaces/unicode-服务器.txt"}
        self.assertEqual({item["path"] for item in manifest["files"]}, expected)
        with zipfile.ZipFile(self.output / exporter.SOURCE_NAME) as archive:
            self.assertIsNone(archive.testzip())
            for item in manifest["files"]:
                data = archive.read(exporter.PREFIX + item["path"])
                self.assertNotIn(self.secret, data)
                self.assertEqual(item["sha256"], hashlib.sha256(data).hexdigest())
                self.assertEqual(item["bytes"], len(data))
                # Compare archived bytes against the committed object, not a
                # potentially filtered working-tree copy.
                self.assertEqual(item["git_blob"], self.git("rev-parse", "HEAD:" + item["path"]).strip())
        for artifact in manifest["artifacts"]:
            data = (self.output / artifact["file"]).read_bytes()
            self.assertEqual(artifact["sha256"], hashlib.sha256(data).hexdigest())
        restored = self.root / "restored bundle"
        self.git("clone", "--bare", str(self.output / exporter.BUNDLE_NAME), str(restored))
        self.assertEqual(self.git("rev-parse", "HEAD", repo=restored).strip(), self.head)
        self.assertEqual(self.git("rev-parse", "HEAD^{tree}", repo=restored).strip(), self.tree)
        self.assertEqual(self.git("rev-parse", "HEAD^", repo=restored).strip(), self.first)
        for name in (".env", "media/private.iso", "database/private.dump", "toolchain/private.exe"):
            result = exporter.Git(restored).run("cat-file", "-e", self.head + ":" + name, allow_failure=True)
            self.assertNotEqual(result.returncode, 0, "Ignored secret entered bundle")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual((self.repo / ".env").read_bytes(), self.secret)

    def test_global_autocrlf_clean_crlf_checkout_exports_committed_lf(self):
        self.git("config", "--unset", "core.autocrlf")
        home = self.root / "isolated user config"
        home.mkdir()
        (home / ".gitconfig").write_text("[core]\n\tautocrlf = true\n\teol = crlf\n", encoding="utf-8")
        env = {key: value for key, value in os.environ.items() if not key.upper().startswith("GIT_")}
        env.update({"HOME": str(home), "USERPROFILE": str(home), "XDG_CONFIG_HOME": str(home / ".config"), "GIT_OPTIONAL_LOCKS": "0"})
        # Re-check out the committed LF objects using the isolated global
        # configuration. Only these explicitly created fixture files are removed.
        for name in (".gitignore", "tracked.txt", "source with spaces/unicode-服务器.txt"):
            (self.repo / name).unlink()
        for args in (("checkout-index", "--all"), ("add", "--renormalize", ".")):
            prepared = subprocess.run(["git", "-C", str(self.repo), *args], env=env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
            self.assertEqual(prepared.returncode, 0, prepared.stderr.decode(errors="replace"))
        self.assertEqual((self.repo / "tracked.txt").read_bytes(), b"Current committed source\r\n")
        baseline = subprocess.run(["git", "-C", str(self.repo), "status", "--porcelain"], env=env,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
        self.assertEqual(baseline.returncode, 0, baseline.stderr.decode(errors="replace"))
        self.assertEqual(baseline.stdout, b"", "Global autocrlf fixture is not clean")
        # Arbitrary GIT_* overrides must still not redirect the exporter or
        # override the safely read configuration values.
        env.update({"GIT_DIR": str(self.root / "not-a-repository"), "GIT_CONFIG_COUNT": "1",
                    "GIT_CONFIG_KEY_0": "core.autocrlf", "GIT_CONFIG_VALUE_0": "false"})
        manifest = self.invoke(env=env)
        self.assertEqual(manifest["snapshot"]["local_head"], self.head)
        self.assertEqual(manifest["snapshot"]["worktree_line_endings"], {"core.autocrlf": "true", "core.eol": "crlf"})
        with zipfile.ZipFile(self.output / exporter.SOURCE_NAME) as archive:
            self.assertEqual(archive.read(exporter.PREFIX + "tracked.txt"), b"Current committed source\n")
        self.assertEqual((self.repo / "tracked.txt").read_bytes(), b"Current committed source\r\n")

    def test_line_ending_config_whitelist_excludes_helpers_and_rejects_invalid_values(self):
        self.git("config", "--unset", "core.autocrlf")
        home = self.root / "config whitelist fixture"
        home.mkdir()
        config = home / ".gitconfig"
        for key, value in (("core.autocrlf", "false"), ("core.eol", "lf"),
                           ("filter.private.clean", "must-not-inherit-this-filter"),
                           ("credential.helper", "must-not-inherit-this-helper"),
                           ("core.hooksPath", str(self.root / "must-not-inherit-hooks"))):
            self.git("config", "--file", str(config), key, value)
        env = {key: value for key, value in os.environ.items() if not key.upper().startswith("GIT_")}
        env.update({"HOME": str(home), "USERPROFILE": str(home), "XDG_CONFIG_HOME": str(home / ".config")})
        with patch.dict(os.environ, env, clear=True):
            git = exporter.Git(self.repo)
            git.line_endings = git.effective_line_endings()
            self.assertEqual(git.line_endings, {"core.autocrlf": "false", "core.eol": "lf"})
            for key in ("filter.private.clean", "credential.helper"):
                self.assertEqual(git.run("config", "--get", key, allow_failure=True).returncode, 1)
            self.assertEqual(git.text("config", "--get", "core.hooksPath"), os.devnull)
        self.git("config", "--file", str(config), "core.eol", "unsupported-fixture-value")
        message = self.invoke(env=env, success=False)
        self.assertIn("Unsupported effective Git line-ending value for core.eol", message)
        self.assertFalse(self.output.exists())

    def test_dirty_unstaged_staged_and_untracked_are_rejected(self):
        for kind in ("unstaged", "staged", "untracked"):
            with self.subTest(kind=kind):
                if kind == "untracked":
                    path = self.repo / "new file.txt"
                else:
                    path = self.repo / "tracked.txt"
                path.write_text("Uncommitted fixture\n", encoding="utf-8")
                if kind == "staged":
                    self.git("add", "tracked.txt")
                message = self.invoke(success=False)
                self.assertIn("Dirty worktree", message)
                self.assertFalse(self.output.exists())
                if kind == "untracked":
                    path.unlink()
                else:
                    self.git("restore", "--source=HEAD", "--staged", "--worktree", "tracked.txt")

    def test_output_guards_preserve_existing_directory(self):
        self.output.mkdir()
        marker = self.output / "keep.txt"
        marker.write_text("Existing output must survive", encoding="utf-8")
        self.assertIn("already exists", self.invoke(success=False))
        self.assertEqual(marker.read_text(encoding="utf-8"), "Existing output must survive")
        for output in (self.repo / "ignored" / "new-delivery", self.repo / ".git" / "new-delivery"):
            with self.subTest(output=output):
                output.parent.mkdir(exist_ok=True)
                self.assertIn("outside", self.invoke(output=output, success=False))
                self.assertFalse(output.exists())
        traversed = str(self.root / "ignored-name") + os.sep + ".." + os.sep + "traversed-output"
        self.assertIn("traversal", self.invoke(output=traversed, success=False))
        self.assertFalse((self.root / "traversed-output").exists())

    def test_output_parent_symlink_cannot_hide_repo_location(self):
        alias = self.root / "repo alias"
        try:
            alias.symlink_to(self.repo, target_is_directory=True)
        except OSError as exc:
            self.skipTest("Directory symlink creation unavailable: " + str(exc))
        self.assertIn("outside", self.invoke(output=alias / "delivery", success=False))
        self.assertFalse((self.repo / "delivery").exists())

    def test_detached_head_and_full_sha_pin(self):
        self.git("checkout", "--detach", self.head)
        manifest = self.invoke("--expected-head", self.head)
        self.assertIsNone(manifest["snapshot"]["branch"])
        self.assertEqual(manifest["snapshot"]["local_head"], self.head)
        for expected in (self.first, self.head[:12], "HEAD", "--all"):
            with self.subTest(expected=expected):
                message = self.invoke("--expected-head=" + expected, output=self.root / ("bad-" + expected[:12]), success=False)
                self.assertIn("--expected-head", message)

    def test_local_remote_ref_comparison_does_not_contact_remote(self):
        self.git("remote", "add", "origin", "https://example.invalid/never-contact-this-repository.git")
        remote_head = self.git("commit-tree", self.tree, "-p", self.first, "-m", "Independent publication history").strip()
        self.assertNotEqual(remote_head, self.head)
        self.git("update-ref", "refs/remotes/origin/codex/delivery", remote_head)
        manifest = self.invoke("--remote-ref", "refs/remotes/origin/codex/delivery")
        self.assertEqual(manifest["remote_comparison"]["status"], "SAME_TREE")
        self.assertEqual(manifest["remote_comparison"]["commit"], remote_head)
        self.assertFalse(manifest["remote_comparison"]["network_accessed"])
        self.git("update-ref", "refs/remotes/origin/codex/delivery", self.first)
        self.assertIn("different tree", self.invoke("--remote-ref", "refs/remotes/origin/codex/delivery", output=self.root / "different", success=False))
        self.assertIn("exact local", self.invoke("--remote-ref", "https://example.invalid/repo", output=self.root / "url", success=False))
        self.invoke("--remote-ref", "refs/remotes/origin/missing", output=self.root / "missing-ref", success=False)

    def test_evidence_is_explicit_report_not_a_test_execution(self):
        path = self.root / "redacted evidence.json"
        payload = {"checks": [{"name": "Local test report", "reported_status": "PASS", "scope": "PostgreSQL fixture only",
                               "evidence": "Retained run identifier: fixture-42", "tested_commit": self.first}]}
        path.write_text(json.dumps(payload), encoding="utf-8")
        manifest = self.invoke("--evidence", str(path))
        self.assertEqual(manifest["test_evidence"]["verification"], "NOT_PERFORMED_BY_EXPORTER")
        self.assertFalse(manifest["test_evidence"]["checks"][0]["matches_exported_head"])
        self.assertEqual(manifest["test_evidence"]["checks"][0]["reported_status"], "PASS")
        self.assertEqual(manifest["test_evidence"]["input_sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
        self.assertFalse(manifest["artifact_verification"]["application_tests_run"])
        self.assertFalse((self.output / path.name).exists())
        payload["password"] = "Do not copy unknown fields"
        path.write_text(json.dumps(payload), encoding="utf-8")
        self.invoke("--evidence", str(path), output=self.root / "invalid-evidence", success=False)
        self.assertFalse((self.root / "invalid-evidence").exists())
        self.invoke("--evidence", str(self.repo / ".env"), output=self.root / "not-json", success=False)

    def test_export_attributes_cannot_omit_or_rewrite_committed_bytes(self):
        for attribute in ("export-ignore", "export-subst"):
            with self.subTest(attribute=attribute):
                (self.repo / ".gitattributes").write_text("tracked.txt " + attribute + "\n", encoding="utf-8")
                (self.repo / "tracked.txt").write_text("$Format:%H$\n", encoding="utf-8")
                self.git("add", ".gitattributes", "tracked.txt")
                self.git("commit", "-m", "Attribute fixture " + attribute)
                output = self.root / attribute
                message = self.invoke(output=output, success=False)
                self.assertIn(attribute, message)
                self.assertTrue((output / "EXPORT-INCOMPLETE").exists())
                self.assertTrue((output / "export-failure.json").exists())
                self.assertFalse((output / "manifest.json").exists())

    def test_unsupported_index_flags_submodules_and_symlinks_are_rejected(self):
        for flag in ("assume-unchanged", "skip-worktree"):
            with self.subTest(flag=flag):
                self.git("update-index", "--" + flag, "tracked.txt")
                self.assertIn("index entries", self.invoke(success=False))
                self.git("update-index", "--no-" + flag, "tracked.txt")
        self.git("update-index", "--add", "--cacheinfo", "160000," + self.head + ",embedded")
        self.git("commit", "-m", "Unpopulated submodule fixture")
        (self.repo / "embedded").mkdir()
        self.assertIn("submodules", self.invoke(success=False))
        self.git("update-index", "--force-remove", "embedded")
        # An index-only symlink fixture works even on Windows without symlink
        # privileges. core.symlinks=false materializes its contents as a file.
        oid = self.git("rev-parse", "HEAD:tracked.txt").strip()
        self.git("update-index", "--add", "--cacheinfo", "120000," + oid + ",linked")
        self.git("commit", "-m", "Symlink fixture")
        self.git("config", "core.symlinks", "false")
        self.git("checkout-index", "--force", "linked")
        self.assertIn("Symlinks", self.invoke(success=False))
        self.assertFalse(self.output.exists())

    def test_unsafe_archive_paths_and_corrupt_artifacts_are_rejected(self):
        for name in ("../escape", "/absolute", "C:/stream", "a\\escape", "a/../escape", ".git/config", "NUL.txt", "trailing. ", "a//b", "line\nbreak"):
            with self.subTest(name=name), self.assertRaises(exporter.ExportError):
                exporter.safe_source_path(name)
        manifest = self.invoke()
        corrupted = self.root / "corrupted.zip"
        with zipfile.ZipFile(corrupted, "w") as archive:
            for item in manifest["files"]:
                archive.writestr(exporter.PREFIX + item["path"], b"Changed bytes")
        expected = {item["path"]: {"git_blob": item["git_blob"], "git_mode": item["git_mode"]} for item in manifest["files"]}
        with self.assertRaises(exporter.ExportError):
            exporter.verify_archive(corrupted, expected, "sha1")
        broken_bundle = self.root / "corrupted.bundle"
        broken_bundle.write_bytes((self.output / exporter.BUNDLE_NAME).read_bytes()[:100])
        with self.assertRaises(exporter.ExportError):
            exporter.verify_bundle(broken_bundle, manifest["snapshot"], "sha1", 30)

    def test_nested_repo_directory_and_default_repo_argument(self):
        manifest = self.invoke(repo=self.repo / "source with spaces")
        self.assertEqual(manifest["snapshot"]["tree"], self.tree)
        output = self.root / "current-directory-export"
        result = subprocess.run([sys.executable, str(SCRIPT), "--output", str(output)], cwd=self.repo,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))


if __name__ == "__main__":
    unittest.main()
