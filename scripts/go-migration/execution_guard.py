#!/usr/bin/env python3
"""Cooperative, fail-closed ownership for Chat writers across all worktrees.

Use the ONE explicitly supplied state directory at the repository root. Atomic
mkdir chooses a single claimant; the JSON receipt alone is not a lock. Expiry
never authorizes stealing a lock. Stale recovery requires a separately reviewed
terminal run, process/worktree attribution and preservation of the old receipt.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import uuid

LOCK = "dokitool-chat-execution.lock"
RECEIPT = "dokitool-chat-execution.json"


class OwnershipError(RuntimeError):
    pass


def now() -> datetime:
    return datetime.now(timezone.utc)


def read(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8-sig"))
        if not isinstance(value, dict):
            raise ValueError("not an object")
        return value
    except (OSError, ValueError, UnicodeError) as exc:
        raise OwnershipError("ownership record unavailable or malformed") from exc


def write(path: Path, value: dict) -> None:
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    try:
        with temporary.open("x", encoding="utf-8", newline="\n") as stream:
            json.dump(value, stream, indent=2, ensure_ascii=False)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def acquire(state: Path, owner: str, worktree: str, seconds: int = 3600) -> dict:
    if not owner.strip() or not worktree.strip() or not 60 <= seconds <= 14400:
        raise OwnershipError("owner/worktree required; lease must be 60..14400 seconds")
    state.mkdir(parents=True, exist_ok=True)
    lock = state / LOCK
    try:
        lock.mkdir()  # One atomic operation; never exists_ok, replace or auto-steal.
    except FileExistsError as exc:
        raise OwnershipError("writer lock exists; do not steal, even after expiry") from exc
    nonce = uuid.uuid4().hex
    try:
        receipt = state / RECEIPT
        previous = read(receipt) if receipt.exists() else None
        if previous is not None and previous.get("status") != "idle":
            raise OwnershipError("legacy receipt is not idle; reconcile before claiming")
        record = {"owner": owner, "nonce": nonce, "status": "active", "worktree": worktree,
                  "started_at": now().isoformat(),
                  "lease_until": (now() + timedelta(seconds=seconds)).isoformat()}
        write(lock / "owner.json", {"owner": owner, "nonce": nonce})
        write(receipt, record)
        return record
    except BaseException:
        # This invocation created the directory. Do not remove unexpected files.
        marker = lock / "owner.json"
        if marker.exists() and read(marker).get("nonce") == nonce:
            marker.unlink()
        lock.rmdir()
        raise


def owned(state: Path, owner: str, nonce: str, *, unexpired: bool = True) -> dict:
    marker = read(state / LOCK / "owner.json")
    record = read(state / RECEIPT)
    if (not owner or not nonce or marker.get("owner") != owner or marker.get("nonce") != nonce
            or record.get("owner") != owner or record.get("nonce") != nonce
            or record.get("status") != "active"):
        raise OwnershipError("owner/nonce mismatch; no write or release permitted")
    if unexpired:
        try:
            deadline = datetime.fromisoformat(record["lease_until"].replace("Z", "+00:00"))
            valid = deadline.tzinfo is not None and deadline > now()
        except (KeyError, ValueError, TypeError, AttributeError):
            valid = False
        if not valid:
            raise OwnershipError("lease expired or invalid; stop and reconcile")
    return record


def release(state: Path, owner: str, nonce: str, head: str, next_work: str) -> dict:
    record = owned(state, owner, nonce, unexpired=False)
    if len(head) != 40 or any(c not in "0123456789abcdef" for c in head):
        raise OwnershipError("release requires the actual full Git SHA")
    record.update(status="idle", finished_at=now().isoformat(), lease_until=now().isoformat(),
                  head=head, next_work=next_work)
    write(state / RECEIPT, record)
    (state / LOCK / "owner.json").unlink()
    (state / LOCK).rmdir()
    return record


def main() -> int:
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("operation", choices=("acquire", "check", "release"))
    cli.add_argument("--state-dir", type=Path, required=True)
    cli.add_argument("--owner", required=True)
    cli.add_argument("--nonce", default="")
    cli.add_argument("--worktree", default="")
    cli.add_argument("--lease-seconds", type=int, default=3600)
    cli.add_argument("--head", default="")
    cli.add_argument("--next-work", default="")
    args = cli.parse_args()
    try:
        state = args.state_dir.resolve()
        if args.operation == "acquire":
            result = acquire(state, args.owner, args.worktree, args.lease_seconds)
        elif args.operation == "check":
            result = owned(state, args.owner, args.nonce)
        else:
            result = release(state, args.owner, args.nonce, args.head, args.next_work)
        print(json.dumps(result, ensure_ascii=False))
        return 0
    except (OwnershipError, OSError) as exc:
        print("OWNERSHIP_REFUSED: " + str(exc))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
