#!/usr/bin/env python3
"""Real process competition plus ownership/expiry failure regressions."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
import threading
from unittest import mock

SCRIPT = Path(__file__).with_name("execution_guard.py")
SPEC = importlib.util.spec_from_file_location("guard", SCRIPT)
guard = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guard)
HEAD = "a" * 40


class OwnershipTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="dokitool-ownership-")
        self.addCleanup(self.temp.cleanup)
        self.state = Path(self.temp.name)

    def claim(self):
        return guard.acquire(self.state, "first", "fixture-worktree")

    def test_claim_check_release_and_next_owner(self):
        record = self.claim()
        self.assertEqual(guard.owned(self.state, "first", record["nonce"]), record)
        result = guard.release(self.state, "first", record["nonce"], HEAD, "next step")
        self.assertEqual(result["status"], "idle")
        self.assertEqual(result["head"], HEAD)
        self.assertFalse((self.state / guard.LOCK).exists())
        second = guard.acquire(self.state, "second", "another-worktree")
        self.assertNotEqual(record["nonce"], second["nonce"])

    def test_existing_lock_blocks_even_without_marker(self):
        (self.state / guard.LOCK).mkdir()
        with self.assertRaises(guard.OwnershipError):
            self.claim()
        self.assertTrue((self.state / guard.LOCK).is_dir())

    def test_legacy_active_is_not_overwritten_even_if_expired(self):
        old = {"owner": "legacy", "status": "active", "lease_until": "2000-01-01T00:00:00Z"}
        guard.write(self.state / guard.RECEIPT, old)
        with self.assertRaises(guard.OwnershipError):
            self.claim()
        self.assertEqual(guard.read(self.state / guard.RECEIPT), old)
        self.assertFalse((self.state / guard.LOCK).exists())

    def test_foreign_owner_cannot_release_or_modify_receipt(self):
        record = self.claim()
        before = (self.state / guard.RECEIPT).read_bytes()
        for owner, nonce in (("other", record["nonce"]), ("first", "wrong")):
            with self.subTest(owner=owner), self.assertRaises(guard.OwnershipError):
                guard.release(self.state, owner, nonce, HEAD, "steal")
        self.assertEqual((self.state / guard.RECEIPT).read_bytes(), before)

    def test_receipt_changed_by_legacy_writer_stops_current_owner(self):
        record = self.claim()
        changed = dict(record, owner="legacy-writer")
        guard.write(self.state / guard.RECEIPT, changed)
        with self.assertRaises(guard.OwnershipError):
            guard.owned(self.state, "first", record["nonce"])
        with self.assertRaises(guard.OwnershipError):
            guard.release(self.state, "first", record["nonce"], HEAD, "")
        self.assertEqual(guard.read(self.state / guard.RECEIPT), changed)

    def test_expired_owner_must_stop_but_can_release_own_record(self):
        record = self.claim()
        guard.write(self.state / guard.RECEIPT, dict(record, lease_until="2000-01-01T00:00:00Z"))
        with self.assertRaises(guard.OwnershipError):
            guard.owned(self.state, "first", record["nonce"])
        with self.assertRaises(guard.OwnershipError):
            guard.acquire(self.state, "second", "elsewhere")
        self.assertEqual(guard.release(self.state, "first", record["nonce"], HEAD, "review")["status"], "idle")

    def test_bad_receipt_and_bad_release_sha_fail_closed(self):
        (self.state / guard.RECEIPT).write_text("not-json")
        with self.assertRaises(guard.OwnershipError):
            self.claim()
        self.assertEqual((self.state / guard.RECEIPT).read_text(), "not-json")
        (self.state / guard.RECEIPT).unlink()
        record = self.claim()
        with self.assertRaises(guard.OwnershipError):
            guard.release(self.state, "first", record["nonce"], "short", "")
        self.assertEqual(guard.owned(self.state, "first", record["nonce"]), record)

    def test_invalid_claim_never_creates_lock(self):
        for owner, seconds in (("", 3600), ("writer", 0), ("writer", 14401)):
            with self.subTest(owner=owner, seconds=seconds), self.assertRaises(guard.OwnershipError):
                guard.acquire(self.state, owner, "worktree", seconds)
        self.assertFalse((self.state / guard.LOCK).exists())


    def test_duplicate_release_preserves_successor(self):
        first = self.claim()
        entered, resume = threading.Event(), threading.Event()
        original = guard.owned
        result = {}
        def delayed(*args, **kwargs):
            record = original(*args, **kwargs)
            if threading.current_thread().name == "late-release":
                entered.set()
                if not resume.wait(10):
                    raise RuntimeError("test barrier timed out")
            return record
        def late():
            try:
                result["late"] = guard.release(self.state, "first", first["nonce"], HEAD, "late")
            except Exception as exc:
                result["error"] = str(exc)
        with mock.patch.object(guard, "owned", side_effect=delayed):
            thread = threading.Thread(target=late, name="late-release"); thread.start()
            successor = None
            try:
                self.assertTrue(entered.wait(5))
                try:
                    guard.release(self.state, "first", first["nonce"], HEAD, "duplicate")
                    result["duplicate_accepted"] = True
                except guard.OwnershipError:
                    result["duplicate_accepted"] = False
                try:
                    successor = guard.acquire(self.state, "second", "fixture")
                except guard.OwnershipError:
                    pass
            finally:
                resume.set(); thread.join(10)
            self.assertFalse(thread.is_alive())
        if successor is None:
            successor = guard.acquire(self.state, "second", "fixture")
        self.assertEqual(guard.owned(self.state, "second", successor["nonce"]), successor)
        self.assertFalse(result["duplicate_accepted"])
        self.assertNotIn("error", result)
        before = (self.state / guard.RECEIPT).read_bytes()
        with self.assertRaises(guard.OwnershipError):
            guard.release(self.state, "first", first["nonce"], HEAD, "stale retry")
        self.assertEqual((self.state / guard.RECEIPT).read_bytes(), before)

    def test_lifecycle_busy_refuses_mutations_without_writes(self):
        first = self.claim()
        gate = self.state / "dokitool-chat-execution.lifecycle.lock"; gate.mkdir()
        before = (self.state / guard.RECEIPT).read_bytes()
        try:
            for operation in (lambda: guard.acquire(self.state, "second", "fixture"),
                              lambda: guard.release(self.state, "first", first["nonce"], HEAD, "")):
                with self.assertRaises(guard.OwnershipError):
                    operation()
            self.assertEqual((self.state / guard.RECEIPT).read_bytes(), before)
            self.assertEqual(guard.owned(self.state, "first", first["nonce"]), first)
        finally:
            gate.rmdir()

    def test_failed_release_keeps_owner_and_cleans_lifecycle(self):
        first = self.claim()
        with mock.patch.object(guard, "write", side_effect=OSError("synthetic write failure")):
            with self.assertRaises(OSError):
                guard.release(self.state, "first", first["nonce"], HEAD, "")
        self.assertEqual(guard.owned(self.state, "first", first["nonce"]), first)
        self.assertFalse((self.state / "dokitool-chat-execution.lifecycle.lock").exists())
        guard.release(self.state, "first", first["nonce"], HEAD, "retry")

    def test_acquire_cannot_enter_before_release_cleanup_finishes(self):
        first = self.claim(); original = Path.rmdir; results = []
        def after_remove(path):
            value = original(path)
            if path == self.state / guard.LOCK:
                try:
                    results.append(guard.acquire(self.state, "second", "fixture"))
                except guard.OwnershipError:
                    results.append("refused")
            return value
        with mock.patch.object(Path, "rmdir", after_remove):
            guard.release(self.state, "first", first["nonce"], HEAD, "")
        self.assertEqual(results, ["refused"])
        second = guard.acquire(self.state, "second", "fixture")
        self.assertEqual(guard.owned(self.state, "second", second["nonce"]), second)


    def test_duplicate_release_across_real_processes(self):
        first = self.claim()
        child = r'''import importlib.util,pathlib,sys,time
p,state,nonce=sys.argv[1:]; state=pathlib.Path(state)
spec=importlib.util.spec_from_file_location("g",p); g=importlib.util.module_from_spec(spec); spec.loader.exec_module(g)
original=g.owned
def delayed(*args,**kwargs):
 record=original(*args,**kwargs); (state/"release-checked").touch(); deadline=time.monotonic()+15
 while not (state/"release-resume").exists():
  if time.monotonic()>deadline: raise RuntimeError("barrier expired")
  time.sleep(0.01)
 return record
g.owned=delayed
g.release(state,"first",nonce,"a"*40,"child release")
'''
        process = subprocess.Popen([sys.executable, "-c", child, str(SCRIPT), str(self.state), first["nonce"]],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 10
            while not (self.state / "release-checked").exists():
                self.assertIsNone(process.poll())
                self.assertLess(time.monotonic(), deadline)
                time.sleep(0.01)
            for args in (["release", "--owner", "first", "--nonce", first["nonce"], "--head", HEAD],
                         ["acquire", "--owner", "second", "--worktree", "fixture"]):
                result = subprocess.run([sys.executable, str(SCRIPT), *args, "--state-dir", str(self.state)],
                                        capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 2, result.stdout)
                self.assertIn(b"lifecycle", result.stdout)
        finally:
            (self.state / "release-resume").touch()
            try:
                out, err = process.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill(); process.communicate(timeout=5); raise
        self.assertEqual(process.returncode, 0, err)
        second = guard.acquire(self.state, "second", "fixture")
        with self.assertRaises(guard.OwnershipError):
            guard.release(self.state, "first", first["nonce"], HEAD, "late retry")
        self.assertEqual(guard.owned(self.state, "second", second["nonce"]), second)

    def test_simultaneous_processes_have_exactly_one_winner(self):
        child = '''import importlib.util,pathlib,sys,time,json
p,state,i=sys.argv[1:]; state=pathlib.Path(state)
spec=importlib.util.spec_from_file_location("g",p); g=importlib.util.module_from_spec(spec); spec.loader.exec_module(g)
(state/("ready-"+i)).touch(); deadline=time.monotonic()+15
while not (state/"start").exists():
 if time.monotonic()>deadline: raise SystemExit(9)
 time.sleep(0.01)
try: print(json.dumps(g.acquire(state,"worker-"+i,"fixture"))); raise SystemExit(0)
except g.OwnershipError: raise SystemExit(2)
'''
        children = [subprocess.Popen([sys.executable, "-c", child, str(SCRIPT), str(self.state), str(i)],
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE) for i in range(6)]
        try:
            deadline = time.monotonic() + 15
            while len(list(self.state.glob("ready-*"))) != len(children):
                self.assertLess(time.monotonic(), deadline, "children did not reach barrier")
                time.sleep(0.01)
            (self.state / "start").touch()
            results = []
            for process in children:
                out, err = process.communicate(timeout=20)
                results.append((process.returncode, out, err))
            self.assertEqual(sorted(code for code, _, _ in results), [0, 2, 2, 2, 2, 2])
            winner = json.loads(next(out for code, out, _ in results if code == 0))
            self.assertEqual(guard.owned(self.state, winner["owner"], winner["nonce"]), winner)
        finally:
            for process in children:
                if process.poll() is None:
                    process.kill(); process.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main()
