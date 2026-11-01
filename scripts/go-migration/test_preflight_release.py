#!/usr/bin/env python3
"""Unit checks for the production-independent Go release preflight."""

import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("preflight_release.py")
SPEC = importlib.util.spec_from_file_location("dokitool_preflight", SCRIPT)
assert SPEC and SPEC.loader
preflight = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(preflight)


def catalog(ids):
    return {
        "schema_version": "1.0", "code": "OK",
        "data": {"platforms": [{"id": value} for value in ids]},
    }


class PreflightUnitTests(unittest.TestCase):
    def test_nine_distinct_registered_platforms(self):
        actual = preflight.check_catalog(catalog(sorted(preflight.EXPECTED_PLATFORMS)))
        self.assertEqual(actual, preflight.EXPECTED_PLATFORMS)

    def test_missing_and_duplicate_platform_rejected(self):
        ids = sorted(preflight.EXPECTED_PLATFORMS)
        for broken in (ids[:-1], [*ids[:-1], ids[0]], [*ids, "unknown"]):
            with self.subTest(broken=broken), self.assertRaises(preflight.PreflightError):
                preflight.check_catalog(catalog(broken))

    def test_envelope_and_platform_shape_rejected(self):
        ids = sorted(preflight.EXPECTED_PLATFORMS)
        for broken in (None, [], {}, {"schema_version": "2.0", "code": "OK"},
                       {"schema_version": "1.0", "code": "OK", "data": {"platforms": ids}}):
            with self.subTest(broken=broken), self.assertRaises(preflight.PreflightError):
                preflight.check_catalog(broken)

    def test_stage_never_under_tracked_tree(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.assertEqual(preflight.validate_stage_dir(root, root / ".local" / "release"), root / ".local" / "release")
            for dest in (root, root / "backend" / "candidate", root / "ops" / "release"):
                with self.subTest(dest=dest), self.assertRaises(preflight.PreflightError):
                    preflight.validate_stage_dir(root, dest)

    def test_hash_digest_is_real_file_hash(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "payload"
            path.write_bytes(b"abc")
            self.assertEqual(
                preflight.digest(path),
                {"file": "payload", "bytes": 3,
                 "sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
            )

    def test_json_rejects_scalar_and_corruption(self):
        for payload in ('not JSON', '["array"]', '"string"', b"\xff"):
            with self.subTest(payload=payload), self.assertRaises(preflight.PreflightError):
                preflight.read_json(payload)


class SourceIsolationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="preflight-unit-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.git("init", "--quiet")
        self.git("config", "core.autocrlf", "false")
        files = {
            ".gitignore": b"/backend/*\n!/backend/go.mod\n!/backend/go.sum\n!/backend/main.go\n!/backend/data.bin\n",
            "backend/go.mod": b"module example.invalid/preflight-fixture\n\ngo 1.27.0\n",
            "backend/go.sum": b"",
            "backend/main.go": b"package main\nfunc main() {}\n",
            "backend/data.bin": b"\x00\xfffixture\r\n",
            "scripts/go-migration/preflight_release.py": b"# committed recipe fixture\n",
        }
        for name, content in files.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
        self.git("add", "-f", "--", *files)
        self.commit()
        self.head = self.git("rev-parse", "HEAD").strip()

    def git(self, *args, input=None):
        # Git plumbing consumes exact bytes. Text-mode stdin on Windows would
        # turn mktree record separators into CRLF and append CR to filenames.
        data = input.encode("utf-8") if isinstance(input, str) else input
        result = subprocess.run(
            ["git", "-C", str(self.root), *args], input=data,
            capture_output=True, check=True, timeout=20,
        )
        return result.stdout.decode("utf-8")

    def commit(self):
        self.git("-c", "user.name=Preflight Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "isolated fixture")

    def snapshot(self):
        target = Path(self.temp.name) / "snapshot"
        report = preflight.snapshot_backend(self.root, self.head, target)
        return target, report

    def test_ignored_source_cannot_enter_clean_snapshot(self):
        extra = self.root / "backend" / "unexpected.go"
        extra.write_text("package main\nfunc init() { panic(\"uncommitted\") }\n")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertTrue(preflight.git_identity(self.root, allow_dirty=False)[1])
        target, report = self.snapshot()
        self.assertFalse((target / "backend" / extra.name).exists())
        self.assertEqual(report["verified_files"], 4)
        self.assertEqual(report["commit"], self.head)

    def test_working_tree_edit_not_compiled_as_head(self):
        path = self.root / "backend" / "main.go"
        original = path.read_bytes()
        path.write_text("package main\nfunc main() { panic(\"changed\") }\n")
        with self.assertRaises(preflight.PreflightError):
            preflight.git_identity(self.root, allow_dirty=False)
        target, _ = self.snapshot()
        self.assertEqual((target / "backend" / "main.go").read_bytes(), original)

    def test_pinned_commit_survives_checkout_advancing(self):
        path = self.root / "backend" / "main.go"
        original = path.read_bytes()
        path.write_text("package main\nfunc main() { println(\"new\") }\n")
        self.git("add", "backend/main.go")
        self.commit()
        self.assertNotEqual(self.git("rev-parse", "HEAD").strip(), self.head)
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/main.go").read_bytes(), original)

    def test_binary_embed_input_copied_exactly(self):
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")

    def test_export_ignore_cannot_omit_committed_inputs(self):
        (self.root / ".gitattributes").write_text("backend/data.bin export-ignore\n")
        self.git("add", ".gitattributes")
        self.commit()
        self.head = self.git("rev-parse", "HEAD").strip()
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")

    def test_substitution_cannot_change_committed_inputs(self):
        (self.root / ".gitattributes").write_text("backend/data.bin export-subst\n")
        (self.root / "backend/data.bin").write_bytes(b"$Format:%H$\n")
        self.git("add", ".gitattributes", "backend/data.bin")
        self.commit()
        self.head = self.git("rev-parse", "HEAD").strip()
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"$Format:%H$\n")

    def test_checkout_eol_setting_cannot_rewrite_blob_bytes(self):
        self.git("config", "core.eol", "crlf")
        self.git("config", "core.autocrlf", "true")
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/main.go").read_bytes(), b"package main\nfunc main() {}\n")

    def test_symlink_input_rejected_without_following_target(self):
        oid = self.git("hash-object", "-w", "--stdin", input="../../outside\n").strip()
        self.git("update-index", "--add", "--cacheinfo", f"120000,{oid},backend/link")
        self.commit(); self.head = self.git("rev-parse", "HEAD").strip()
        with self.assertRaisesRegex(preflight.PreflightError, "symlink"):
            self.snapshot()

    def test_missing_checksum_file_rejected(self):
        self.git("rm", "backend/go.sum"); self.commit()
        self.head = self.git("rev-parse", "HEAD").strip()
        with self.assertRaisesRegex(preflight.PreflightError, "checksum"):
            self.snapshot()

    def replacement_tree(self, tree_path="backend", name="data.bin"):
        # Only write synthetic objects in this test's disposable repository.
        original = self.git("rev-parse", f"{self.head}:{tree_path}").strip()
        old_blob = self.git("rev-parse", f"{self.head}:{tree_path}/{name}").strip()
        blob = self.git("hash-object", "-w", "--stdin", input="REPLACED_EMBED").strip()
        listing = self.git("ls-tree", original)
        self.assertIn(old_blob, listing)
        replacement = self.git("mktree", input=listing.replace(old_blob, blob)).strip()
        return original, replacement, blob

    def test_tree_replacement_cannot_change_clean_snapshot(self):
        original, replacement, _ = self.replacement_tree()
        self.git("replace", original, replacement)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("show", f"{self.head}:backend/data.bin"), "REPLACED_EMBED")
        self.assertEqual(preflight.git_identity(self.root, allow_dirty=False), (self.head, True))
        target, report = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")
        self.assertEqual(report["backend_tree"], original)

    def test_nested_tree_replacement_cannot_change_embed(self):
        path = self.root / "backend/config/platforms.json"
        path.parent.mkdir()
        path.write_bytes(b'{"fixture":"original"}\n')
        self.git("add", "-f", "backend/config/platforms.json")
        self.commit(); self.head = self.git("rev-parse", "HEAD").strip()
        original, replacement, _ = self.replacement_tree("backend/config", "platforms.json")
        self.git("replace", original, replacement)
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("show", f"{self.head}:backend/config/platforms.json"), "REPLACED_EMBED")
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/config/platforms.json").read_bytes(), path.read_bytes())

    def test_custom_replacement_namespace_cannot_change_snapshot(self):
        original, replacement, _ = self.replacement_tree()
        namespace = "refs/preflight-fixture-replacements/"
        self.git("update-ref", namespace + original, replacement)
        with mock.patch.dict(os.environ, {"GIT_REPLACE_REF_BASE": namespace}):
            os.environ.pop("GIT_NO_REPLACE_OBJECTS", None)
            self.assertEqual(self.git("show", f"{self.head}:backend/data.bin"), "REPLACED_EMBED")
            self.assertEqual(preflight.git_identity(self.root, allow_dirty=False), (self.head, True))
            target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")

    def test_commit_replacement_cannot_change_pinned_source(self):
        original, replacement, _ = self.replacement_tree()
        root_listing = self.git("ls-tree", f"{self.head}^{{tree}}")
        root_tree = self.git("mktree", input=root_listing.replace(original, replacement)).strip()
        commit = self.git("-c", "user.name=Preflight Fixture", "-c", "user.email=fixture@example.invalid",
                          "-c", "commit.gpgsign=false", "commit-tree", root_tree,
                          "-m", "synthetic replacement, not a branch commit").strip()
        self.git("replace", self.head, commit)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.head)
        self.assertEqual(self.git("show", f"{self.head}:backend/data.bin"), "REPLACED_EMBED")
        target, report = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")
        self.assertEqual(report["backend_tree"], original)

    def test_blob_replacement_still_reads_original_bytes(self):
        original = self.git("rev-parse", f"{self.head}:backend/data.bin").strip()
        replacement = self.git("hash-object", "-w", "--stdin", input="REPLACED_EMBED").strip()
        self.git("replace", original, replacement)
        self.assertEqual(self.git("show", f"{self.head}:backend/data.bin"), "REPLACED_EMBED")
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")

    def test_replacement_cannot_hide_staged_and_worktree_edit(self):
        original, replacement, blob = self.replacement_tree()
        self.git("replace", original, replacement)
        (self.root / "backend/data.bin").write_bytes(b"REPLACED_EMBED")
        self.git("update-index", "--cacheinfo", f"100644,{blob},backend/data.bin")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertNotEqual(self.git("--no-replace-objects", "status", "--porcelain"), "")
        with self.assertRaisesRegex(preflight.PreflightError, "uncommitted"):
            preflight.git_identity(self.root, allow_dirty=False)
        self.assertEqual(preflight.git_identity(self.root, allow_dirty=True), (self.head, False))
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")


    def test_replacement_cached_index_is_not_clean(self):
        original, replacement, _ = self.replacement_tree()
        self.git("replace", original, replacement)
        (self.root / "backend/data.bin").write_bytes(b"REPLACED_EMBED")
        self.git("read-tree", "HEAD")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertNotIn(self.git("--no-replace-objects", "rev-parse", f"{self.head}:backend/data.bin").strip(),
                         self.git("--no-replace-objects", "ls-files", "--stage", "backend/data.bin"))
        with self.assertRaisesRegex(preflight.PreflightError, "uncommitted"):
            preflight.git_identity(self.root, allow_dirty=False)
        self.assertEqual(preflight.git_identity(self.root, allow_dirty=True), (self.head, False))
        target, _ = self.snapshot()
        self.assertEqual((target / "backend/data.bin").read_bytes(), b"\x00\xfffixture\r\n")

    def test_custom_cached_index_dirty_even_after_replacement_removed(self):
        original, replacement, _ = self.replacement_tree(); ns = "refs/cached-index-fixture/"
        self.git("update-ref", ns + original, replacement)
        with mock.patch.dict(os.environ, {"GIT_REPLACE_REF_BASE": ns}):
            os.environ.pop("GIT_NO_REPLACE_OBJECTS", None)
            (self.root / "backend/data.bin").write_bytes(b"REPLACED_EMBED")
            self.git("read-tree", "HEAD")
            self.assertEqual(self.git("status", "--porcelain"), "")
            self.git("update-ref", "-d", ns + original)
            with self.assertRaisesRegex(preflight.PreflightError, "uncommitted"):
                preflight.git_identity(self.root, allow_dirty=False)
            self.assertEqual(preflight.git_identity(self.root, allow_dirty=True), (self.head, False))

    def test_index_entries_checked_independently_of_status(self):
        old_checked = preflight.checked
        def hide_status(argv, **kwargs):
            return "" if "status" in argv else old_checked(argv, **kwargs)
        blob = self.git("rev-parse", f"{self.head}:backend/data.bin").strip()
        operations = [
            ("deletion", lambda: self.git("rm", "--cached", "backend/data.bin")),
            ("mode", lambda: self.git("update-index", "--chmod=+x", "backend/data.bin")),
            ("unmerged", lambda: self.git("update-index", "--index-info", input=
                "0 " + "0" * 40 + "\tbackend/data.bin\n100644 " + blob + " 2\tbackend/data.bin\n")),
        ]
        for label, mutate in operations:
            with self.subTest(kind=label):
                self.git("read-tree", self.head); mutate()
                with mock.patch.object(preflight, "checked", side_effect=hide_status):
                    with self.assertRaisesRegex(preflight.PreflightError, "uncommitted"):
                        preflight.git_identity(self.root, allow_dirty=False)

    def test_every_provenance_git_call_disables_replacement(self):
        real_run = subprocess.run
        with mock.patch.object(preflight.subprocess, "run", wraps=real_run) as run:
            preflight.git_identity(self.root, allow_dirty=False)
            self.snapshot()
            preflight.git_identity(self.root, allow_dirty=False)
        commands = [call.args[0] for call in run.call_args_list]
        self.assertTrue(commands)
        for command in commands:
            self.assertEqual(command[:4], ["git", "--no-replace-objects", "-C", str(self.root)])
        self.assertEqual({command[4] for command in commands},
                         {"rev-parse", "status", "ls-files", "ls-tree", "cat-file"})


class BuildAndPublicationTests(unittest.TestCase):
    def test_build_overrides_removed_without_changing_network_policy(self):
        original = {"GOFLAGS": "-overlay=outside.json", "GOWORK": "/other/go.work",
                    "GOENV": "/other/env", "GOOS": "other", "GOARCH": "other",
                    "GOROOT": "/other/toolchain", "GOTOOLCHAIN": "auto",
                    "SNAP_MEDIA_SIGNING_KEY": "fixture-not-a-real-key",
                    "GOPROXY": "off", "GOCACHE": "/cache", "PATH": "/bin"}
        result = preflight.isolated_environment(original)
        self.assertEqual(result["GOFLAGS"], "")
        self.assertEqual(result["GOWORK"], "off")
        self.assertEqual(result["GOENV"], "off")
        self.assertEqual(result["GOTOOLCHAIN"], "local")
        self.assertEqual(result["CGO_ENABLED"], "0")
        for key in ("GOOS", "GOARCH", "GOROOT", "SNAP_MEDIA_SIGNING_KEY"):
            self.assertNotIn(key, result)
        for key in ("GOPROXY", "GOCACHE", "PATH"):
            self.assertEqual(result[key], original[key])
        self.assertEqual(original["GOWORK"], "/other/go.work")

    def test_external_local_module_replacement_is_rejected(self):
        payload = {"Replace": [{"New": {"Path": "../outside"}}]}
        with mock.patch.object(preflight, "checked", return_value=json.dumps(payload)) as run:
            with self.assertRaisesRegex(preflight.PreflightError, "local module"):
                preflight.verify_module_sources("go", Path("backend"), {})
            self.assertEqual(run.call_count, 1)

    def test_versioned_module_replacement_still_verified(self):
        payload = {"Replace": [{"New": {"Path": "example.invalid/module", "Version": "v1.2.3"}}]}
        with mock.patch.object(preflight, "checked", side_effect=[json.dumps(payload), "all modules verified"]) as run:
            preflight.verify_module_sources("go", Path("backend"), {})
            self.assertEqual(run.call_args.args[0], ["go", "mod", "verify"])

    def test_staging_preserves_bytes_and_mode(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / "candidate"; dest = root / "output"; dest.mkdir()
            source.write_bytes(b"candidate-fixture"); source.chmod(0o755)
            target = preflight.stage_artifact(source, dest)
            self.assertEqual(target.read_bytes(), source.read_bytes())
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), stat.S_IMODE(source.stat().st_mode))
            self.assertFalse((dest / "candidate.partial").exists())

    def test_windows_executable_suffix_retained_during_staging(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "candidate.exe"
            dest = root / "output"
            dest.mkdir()
            source.write_bytes(b"executable-suffix-fixture")
            source.chmod(0o755)
            with mock.patch.object(preflight.shutil, "copy2", wraps=preflight.shutil.copy2) as copy:
                target = preflight.stage_artifact(source, dest)
                self.assertEqual(Path(copy.call_args.args[1]).suffix, ".exe")
            self.assertEqual(target.name, source.name)
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), stat.S_IMODE(source.stat().st_mode))
            self.assertFalse((dest / "candidate.exe.partial.exe").exists())

    @unittest.skipIf(os.name == "nt", "POSIX executable mode must be checked on Linux")
    def test_staged_executable_actually_runs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / "candidate"; dest = root / "output"; dest.mkdir()
            source.write_text("#!/bin/sh\nprintf 'published-fixture\\n'\n"); source.chmod(0o755)
            target = preflight.stage_artifact(source, dest)
            result = subprocess.run([str(target)], capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout.strip(), "published-fixture")

    def test_failed_copy_preserves_previous_artifact(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / "candidate"; dest = root / "output"; dest.mkdir()
            source.write_bytes(b"new"); (dest / "candidate").write_bytes(b"old")
            with mock.patch.object(preflight.shutil, "copy2", side_effect=OSError("fixture failure")):
                with self.assertRaises(OSError):
                    preflight.stage_artifact(source, dest)
            self.assertEqual((dest / "candidate").read_bytes(), b"old")
            self.assertFalse((dest / "candidate.partial").exists())


if __name__ == "__main__":
    unittest.main()
