#!/usr/bin/env python3
"""Gate-unit regressions using simulated outputs; not Go product acceptance."""
import copy
import json
from pathlib import Path
import tempfile
import unittest

import public_projection as gate


class PublicProjectionGateTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="dokitool-oracle-test-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.exports = {}
        self.outputs = {}
        self.changes = json.loads((gate.ROOT / gate.MANIFEST).read_text())
        for fixture in (gate.DOUYIN, gate.INTEGERS):
            frozen = json.loads((gate.ROOT / fixture).read_text())
            self.write(fixture, frozen)
            simulated = {"cases": [{"id": c["id"], "normal": copy.deepcopy(c["expected"]["normal"]),
                                     "debug": copy.deepcopy(c["expected"]["debug"])} for c in frozen["cases"]]}
            # Deliberate literal expected outputs for testing the comparison
            # gate. Never run either production projection or gate patching to
            # manufacture these simulated responses.
            for case in simulated["cases"]:
                for view in ("normal", "debug"):
                    if case["id"] == "live/direct-and-still":
                        case[view]["data"]["items"][2]["variants"][0]["delivery"]["resources"][1]["delivery"]["sources"][0]["url"] = "https://v3-web.douyinvod.com/live-3.mp4?x=a%2Bb&mime_type=video_mp4"
                    if case["id"] in {f"source-size/{value}/{kind}" for value in (9007199254740993, 9223372036854775800) for kind in ("integer", "string")}:
                        del case[view]["data"]["items"][0]["variants"][0]["delivery"]["sources"][0]["size_bytes"]
            self.outputs[fixture] = simulated
            self.exports[fixture] = self.root / ("actual-" + Path(fixture).parent.name + ".json")

    def write(self, name, value):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False) + "\n")

    def verify(self):
        self.write(gate.MANIFEST, self.changes)
        for fixture, path in self.exports.items():
            self.write(path, self.outputs[fixture])
        return gate.verify_exports(self.exports, root=self.root)

    def test_all_ten_exact_changes_and_all_other_fields(self):
        result = self.verify()
        self.assertEqual(result["consumed"], 10)
        self.assertEqual(result["compared_responses"], 2 * sum(len(c["cases"]) for c in self.outputs.values()))

    def test_undeclared_change_fails(self):
        case = next(c for c in self.outputs[gate.DOUYIN]["cases"] if c["id"] == "live/direct-and-still")
        case["normal"]["data"]["items"][0]["id"] += "-wrong"
        with self.assertRaisesRegex(ValueError, "Undeclared"):
            self.verify()

    def test_missing_declaration_fails(self):
        self.changes["differences"].pop()
        with self.assertRaisesRegex(ValueError, "Undeclared"):
            self.verify()

    def test_wrong_before_preserves_integer_precision(self):
        self.changes["differences"][2]["before"] = 9007199254740992
        with self.assertRaisesRegex(ValueError, "before mismatch"):
            self.verify()

    def test_mistyped_before_fails(self):
        self.changes["differences"][2]["before"] = "9007199254740993"
        with self.assertRaisesRegex(ValueError, "before mismatch"):
            self.verify()

    def test_duplicate_declaration_fails(self):
        self.changes["differences"].append(copy.deepcopy(self.changes["differences"][0]))
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            self.verify()

    def test_unused_declaration_fails(self):
        extra = copy.deepcopy(self.changes["differences"][0])
        extra["case_id"] = "absent-case"
        self.changes["differences"].append(extra)
        with self.assertRaisesRegex(ValueError, "Unused"):
            self.verify()

    def test_missing_pointer_fails(self):
        self.changes["differences"][0]["path"] += "/absent"
        with self.assertRaisesRegex(ValueError, "target"):
            self.verify()

    def test_incomplete_actual_inventory_fails(self):
        self.outputs[gate.DOUYIN]["cases"].pop()
        with self.assertRaisesRegex(ValueError, "inventory"):
            self.verify()

    def test_duplicate_actual_case_fails(self):
        self.outputs[gate.DOUYIN]["cases"].append(copy.deepcopy(self.outputs[gate.DOUYIN]["cases"][0]))
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.verify()

    def test_boolean_does_not_equal_integer(self):
        self.assertFalse(gate.same_json({"value": 1}, {"value": True}))


if __name__ == "__main__":
    unittest.main()
