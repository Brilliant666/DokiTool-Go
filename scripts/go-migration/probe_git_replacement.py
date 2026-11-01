#!/usr/bin/env python3
"""Compare actual preflight candidates with/without a synthetic Git tree replacement.

Only a new disposable clone below --output-dir receives synthetic objects/refs.
The source checkout and its refs are read-only. No platform or media requests.
A recipe override is explicitly development-only and uses --allow-dirty.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True, help="local repository; never a network URL")
    parser.add_argument("--ref", required=True, help="exact source commit to test")
    parser.add_argument("--go", required=True, help="pinned Go 1.27.3 executable")
    parser.add_argument("--output-dir", type=Path, required=True, help="new directory; existing paths are refused")
    parser.add_argument("--recipe", type=Path, help="optional uncommitted development recipe override")
    args = parser.parse_args()
    source = args.source.resolve(strict=True)
    output = args.output_dir.resolve()
    if output == source or source in output.parents:
        relative = output.relative_to(source)
        if not relative.parts or relative.parts[0] != ".local":
            raise ValueError("inside the source repository, probe outputs must be under .local/")
    output.mkdir(parents=True, exist_ok=False)
    # Keep the experiment in its disposable clone even inside a Git-aware shell.
    env = os.environ.copy()
    for key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
                "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
                "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE"):
        env.pop(key, None)

    def git(root: Path, *arguments: str, data: bytes | None = None, raw: bool = True) -> bytes:
        command = ["git", *(["--no-replace-objects"] if raw else []), "-C", str(root), *arguments]
        result = subprocess.run(command, input=data, env=env, capture_output=True, timeout=90, check=True)
        return result.stdout

    head = git(source, "rev-parse", f"{args.ref}^{{commit}}").decode("ascii").strip()
    if head != args.ref or len(head) != 40:
        raise ValueError("--ref must be the complete source commit SHA")
    clone = output / "source"
    git(source, "clone", "--no-hardlinks", "--no-checkout", "--", str(source), str(clone))
    git(clone, "-c", "core.autocrlf=false", "checkout", "--detach", head)
    # A local checkout setting only; never changes the source or global config.
    git(clone, "config", "core.autocrlf", "false")
    recipe = clone / "scripts/go-migration/preflight_release.py"
    if args.recipe:
        recipe.write_bytes(args.recipe.resolve(strict=True).read_bytes())
    recipe_hash = sha256(recipe.read_bytes())
    initial_status = git(clone, "status", "--porcelain")
    native_name = "dokitool-go.exe" if os.name == "nt" else "dokitool-go"
    marker = b"CHAT_REPLACED_EMBED"

    def run(label: str) -> dict:
        target = output / label
        command = [sys.executable, "-X", "utf8", str(recipe), "--go", args.go,
                   "--output-dir", str(target)]
        if args.recipe:
            command.append("--allow-dirty")
        result = subprocess.run(command, cwd=clone, env=env, capture_output=True, timeout=360)
        log = result.stdout + result.stderr
        (output / f"{label}.log").write_bytes(log)
        record = {"exit_code": result.returncode, "log_sha256": sha256(log)}
        if result.returncode == 0:
            manifest = json.loads(result.stdout)
            binary = target / native_name
            catalog = subprocess.run([str(binary), "catalog"], env=env, capture_output=True, timeout=10)
            record.update(manifest=manifest, binary_sha256=sha256(binary.read_bytes()),
                          mode=oct(stat.S_IMODE(binary.stat().st_mode)), catalog_exit=catalog.returncode,
                          catalog_contains_marker=marker in catalog.stdout)
            if manifest["commit"] != head:
                raise ValueError("manifest source SHA mismatch")
        print(label, result.returncode, flush=True)
        return record

    baseline = run("baseline")
    if baseline["exit_code"] != 0:
        raise RuntimeError("baseline preflight failed; inspect baseline.log")
    path = "backend/config/platforms.json"
    original = git(clone, "show", f"{head}:{path}")
    changed = original.replace(bytes.fromhex("e68a96e99fb3"), marker, 1)
    if changed == original:
        raise ValueError("expected synthetic display-name substitution was not possible")
    original_blob = git(clone, "rev-parse", f"{head}:{path}").strip()
    original_tree = git(clone, "rev-parse", f"{head}:backend/config").strip()
    new_blob = git(clone, "hash-object", "-w", "--stdin", data=changed).strip()
    listing = git(clone, "ls-tree", original_tree.decode("ascii"))
    if original_blob not in listing:
        raise ValueError("original embedded blob not found in tree")
    replacement_tree = git(clone, "mktree", data=listing.replace(original_blob, new_blob)).strip()
    git(clone, "replace", original_tree.decode("ascii"), replacement_tree.decode("ascii"))
    view_changed = git(clone, "show", f"{head}:{path}", raw=False) == changed
    raw_unchanged = git(clone, "show", f"{head}:{path}") == original
    status_unchanged = git(clone, "status", "--porcelain", raw=False) == initial_status
    head_unchanged = git(clone, "rev-parse", "HEAD", raw=False).decode("ascii").strip() == head
    if not all((view_changed, raw_unchanged, status_unchanged, head_unchanged)):
        raise RuntimeError("replacement experiment prerequisites failed")
    replacement = run("replacement")
    passed = (replacement["exit_code"] == 0
              and replacement["binary_sha256"] == baseline["binary_sha256"]
              and replacement["catalog_exit"] == baseline["catalog_exit"] == 0
              and not replacement["catalog_contains_marker"]
              and not baseline["catalog_contains_marker"])
    source_passed = passed
    dirty = None
    index_rejected = None
    if not args.recipe:
        # Exact dot scenario: populate the clone index through the replacement
        # view, then present matching changed worktree bytes. Raw status can
        # incorrectly stay clean despite different explicit object entries.
        git(clone, "read-tree", "HEAD", raw=False)
        (clone / path).write_bytes(changed)
        index = git(clone, "ls-files", "--stage", "-z", "--", path)
        expected = b"100644 " + new_blob + b" 0\t" + path.encode() + b"\0"
        if index != expected or git(clone, "show", f"{head}:{path}") != original:
            raise RuntimeError("dirty-index experiment prerequisites failed")
        dirty = run("dirty-cached-index")
        dirty["raw_status_empty"] = not bool(git(clone, "status", "--porcelain"))
        dirty["head_blob"] = original_blob.decode("ascii")
        dirty["index_blob"] = new_blob.decode("ascii")
        dirty["expected_exit"] = 1
        index_rejected = (dirty["exit_code"] == 1
                          and b"uncommitted" in (output / "dirty-cached-index.log").read_bytes()
                          and not (output / "dirty-cached-index" / "preflight-manifest.json").exists()
                          and not (output / "dirty-cached-index" / native_name).exists())
        passed = source_passed and index_rejected
        print("STRICT_CACHED_INDEX_REJECTED", index_rejected, flush=True)
    report = {
        "schema": "dokitool-preflight-git-replacement-probe-v1",
        "tested_source_commit": head, "recipe_override": bool(args.recipe), "recipe_sha256": recipe_hash,
        "head_unchanged": head_unchanged, "ordinary_git_status_unchanged": status_unchanged,
        "ordinary_git_view_replaced": view_changed, "raw_git_view_unchanged": raw_unchanged,
        "original_tree": original_tree.decode("ascii"), "replacement_tree": replacement_tree.decode("ascii"),
        "original_embed_sha256": sha256(original), "replacement_embed_sha256": sha256(changed),
        "baseline": baseline, "replacement": replacement,
        "source_integrity_requirement_passed": source_passed,
        "strict_index_requirement_passed": index_rejected, "dirty_cached_index": dirty,
        "limits": ["Local clone only; no source branch/ref mutation", "No platform/browser/production validation",
                   "Recipe override is development-only, not final committed preflight" if args.recipe
                   else "Recipe and backend from the specified committed source"],
    }
    (output / "report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print("SOURCE_INTEGRITY_REQUIREMENT", source_passed, flush=True)
    return 0 if passed else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f"PROBE_FAILED: {exc}", file=sys.stderr)
        raise SystemExit(1)
