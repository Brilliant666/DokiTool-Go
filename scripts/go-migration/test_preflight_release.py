#!/usr/bin/env python3
"""Unit checks for the production-independent Go release preflight."""

import importlib.util
from pathlib import Path
import tempfile
import unittest

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


if __name__ == "__main__":
    unittest.main()
