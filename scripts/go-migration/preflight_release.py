#!/usr/bin/env python3
"""Build and smoke-test an isolated Go candidate without touching production.

This is a development preflight, NOT permission to switch traffic. Only a
loopback listener, static/invalid API requests and built-in CLI commands run.
No real platform URLs, PHP process, or media are requested.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
EXPECTED_PLATFORMS = frozenset({
    "douyin", "kuaishou", "bilibili", "xiaohongshu", "toutiao",
    "weibo", "pipixia", "pipigx", "zuiyou",
})
EXPECTED_TOOLCHAIN = "go1.27.3"


class PreflightError(Exception):
    pass


def checked(argv: list[str], *, cwd: Path | None = None,
            env: dict[str, str] | None = None, timeout: float = 180) -> str:
    try:
        result = subprocess.run(
            argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            encoding="utf-8", errors="replace", timeout=timeout, check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise PreflightError(f"command could not complete: {Path(argv[0]).name}: {exc}") from exc
    if result.returncode:
        # Never display the entire environment, credential-bearing arguments,
        # or arbitrary upstream response bodies.
        raise PreflightError(
            f"{Path(argv[0]).name} exited {result.returncode}: {result.stderr.strip()[:280]}"
        )
    return result.stdout.strip()


def git_identity(root: Path, *, allow_dirty: bool) -> tuple[str, bool]:
    repository = Path(checked(["git", "-C", str(root), "rev-parse", "--show-toplevel"])).resolve()
    if repository != root.resolve():
        raise PreflightError("repository root mismatch")
    head = checked(["git", "-C", str(root), "rev-parse", "HEAD"])
    if len(head) != 40 or any(ch not in "0123456789abcdef" for ch in head):
        raise PreflightError("invalid source commit SHA")
    status = checked(["git", "-C", str(root), "status", "--porcelain", "--untracked-files=normal"])
    # The repository ignores all files until explicitly allowed by its
    # inventory. An ignored, untracked preflight script is not a committed
    # release recipe, even when git status reports clean.
    registered = subprocess.run(
        ["git", "-C", str(root), "ls-files", "--error-unmatch",
         "scripts/go-migration/preflight_release.py"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
    ).returncode == 0
    clean = not bool(status) and registered
    if not clean and not allow_dirty:
        raise PreflightError("working tree or preflight recipe is uncommitted; use --allow-dirty for development-only checks")
    return head, clean


def validate_stage_dir(root: Path, directory: Path) -> Path:
    dest = directory.resolve()
    # The repository uses an explicit public file inventory. A candidate
    # executable must never be staged into a tracked/source directory.
    try:
        relative = dest.relative_to(root.resolve())
    except ValueError:
        return dest
    if not relative.parts or relative.parts[0] != ".local":
        raise PreflightError("inside this repository, staging is allowed only under .local/")
    return dest


def check_catalog(payload: object) -> set[str]:
    if not isinstance(payload, dict) or payload.get("code") != "OK" or payload.get("schema_version") != "1.0":
        raise PreflightError("catalog envelope is not unified-v1 OK")
    data = payload.get("data")
    entries = data.get("platforms") if isinstance(data, dict) else None
    if not isinstance(entries, list):
        raise PreflightError("catalog platforms missing")
    ids = [entry.get("id") for entry in entries if isinstance(entry, dict)]
    if (len(ids) != len(entries)
            or any(not isinstance(value, str) or not value for value in ids)
            or len(ids) != len(set(ids)) or set(ids) != EXPECTED_PLATFORMS):
        raise PreflightError("catalog does not contain exactly nine distinct expected platforms")
    return set(ids)


def read_json(raw: str | bytes) -> dict:
    try:
        parsed = json.loads(raw)
    except (ValueError, TypeError, UnicodeError) as exc:
        raise PreflightError("non-JSON preflight response") from exc
    if not isinstance(parsed, dict):
        raise PreflightError("preflight response must be a JSON object")
    return parsed


def request(port: int, method: str, path: str) -> tuple[int, dict[str, str], dict]:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=2.0)
    try:
        conn.request(method, path, headers={"Accept": "application/json"})
        response = conn.getresponse()
        body = response.read(1 << 20)
        if response.read(1):
            raise PreflightError("preflight response exceeded 1 MiB")
        return response.status, {key.lower(): value for key, value in response.getheaders()}, read_json(body)
    finally:
        conn.close()


def available_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
        if not 1024 <= port <= 65535:
            raise PreflightError("unexpected loopback port")
        return port


def check_refusals(binary: Path, port: int, env: dict[str, str]) -> list[str]:
    refusals = [
        (["serve", "-listen", f"0.0.0.0:{port}"], "public bind", "listen must use an explicit loopback IP"),
        (["serve", "-listen", f"127.0.0.1:{port}", "-xhs-page-transport", "auto"], "unknown XHS mode", "xhs-page-transport must be"),
    ]
    verified: list[str] = []
    for args, label, expected_error in refusals:
        try:
            result = subprocess.run(
                [str(binary), *args], env=env, stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                encoding="utf-8", errors="replace", timeout=5, check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise PreflightError(f"{label} validation did not exit promptly: {exc}") from exc
        if result.returncode == 0 or expected_error not in result.stderr:
            raise PreflightError(f"expected safe rejection did not occur: {label}")
        verified.append(label)
    return verified


def smoke(binary: Path, expected_catalog: dict, env: dict[str, str],
          timeout: float) -> dict:
    checks = [
        ("GET", "/api/platforms?schema_version=1.0", 200, "OK"),
        ("GET", "/api/platforms", 409, "INVALID_INPUT"),
        ("GET", "/api/platforms?schema_version=2.0", 409, "INVALID_INPUT"),
        ("POST", "/api/platforms?schema_version=1.0", 405, "INVALID_INPUT"),
        ("GET", "/api/parse?schema_version=1.0", 400, "INVALID_INPUT"),
        ("GET", "/api/resolve?schema_version=1.0", 400, "INVALID_INPUT"),
        ("GET", "/api/douyin", 410, "INVALID_INPUT"),
        ("GET", "/backend/api/douyin/douyin.php", 404, "INVALID_INPUT"),
        ("GET", "/api/%70arse?schema_version=1.0", 404, "INVALID_INPUT"),
    ]
    port = available_port()
    with tempfile.TemporaryFile() as stderr:
        proc = subprocess.Popen(
            [str(binary), "serve", "-listen", f"127.0.0.1:{port}"],
            env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
            stderr=stderr,
        )
        try:
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    raise PreflightError(f"Go verification service exited early: {proc.returncode}")
                try:
                    status, _, body = request(port, "GET", checks[0][1])
                    if status == 200:
                        if body != expected_catalog:
                            raise PreflightError("CLI catalog and HTTP catalog differ")
                        break
                    raise PreflightError(f"catalog startup returned HTTP {status}")
                except (ConnectionError, TimeoutError, OSError):
                    time.sleep(0.1)
            else:
                raise PreflightError("loopback startup exceeded deadline")
            results = []
            for method, path, expected_status, expected_code in checks:
                status, headers, payload = request(port, method, path)
                if status != expected_status or payload.get("code") != expected_code:
                    raise PreflightError(
                        f"route {method} {path} gave {status}/{payload.get('code')}, "
                        f"expected {expected_status}/{expected_code}"
                    )
                if (
                    headers.get("cache-control") != "no-store"
                    or headers.get("x-content-type-options") != "nosniff"
                    or "access-control-allow-origin" in headers
                ):
                    raise PreflightError(f"unsafe API headers on {method} {path}")
                if status == 405 and headers.get("allow") != "GET":
                    raise PreflightError("GET-only method header missing")
                results.append({"method": method, "path": path, "status": status})
            return {"routes": results, "loopback_only": True}
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=7)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=3)


def digest(path: Path) -> dict:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(chunk)
    return {"file": path.name, "bytes": path.stat().st_size, "sha256": hasher.hexdigest()}


def build(go: str, backend: Path, dest: Path, head: str, env: dict[str, str]) -> None:
    checked(
        [go, "build", "-trimpath", "-buildvcs=false",
         "-ldflags", f"-X main.version={head}", "-o", str(dest), "./cmd/dokitool"],
        cwd=backend, env=env, timeout=240,
    )
    if not dest.is_file() or dest.stat().st_size == 0:
        raise PreflightError("Go build did not produce a nonempty executable")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="explicit Go toolchain executable")
    parser.add_argument("--output-dir", type=Path, help="stage only after every native check passes")
    parser.add_argument("--allow-dirty", action="store_true", help="development only; never release from dirty tree")
    parser.add_argument("--linux-amd64", action="store_true", help="also cross-compile Linux amd64; build-only, not runtime-tested")
    parser.add_argument("--startup-timeout", type=float, default=15.0)
    args = parser.parse_args(argv)
    try:
        root = ROOT.resolve()
        head, clean = git_identity(root, allow_dirty=args.allow_dirty)
        target = validate_stage_dir(root, args.output_dir) if args.output_dir else None
        if not 1 <= args.startup_timeout <= 60:
            raise PreflightError("startup-timeout must be within 1..60 seconds")
        go_toolchain = checked([args.go, "version"], timeout=8)
        if f" {EXPECTED_TOOLCHAIN} " not in go_toolchain:
            raise PreflightError(f"expected {EXPECTED_TOOLCHAIN}, got {go_toolchain}")
        native_name = "dokitool-go.exe" if sys.platform == "win32" else "dokitool-go"
        env = os.environ.copy()
        # Do not allow the developer's actual signing key to leak into the
        # isolated loopback smoke service or resulting release receipt.
        env.pop("SNAP_MEDIA_SIGNING_KEY", None)
        with tempfile.TemporaryDirectory(prefix="dokitool-go-preflight-") as tmp:
            stage = Path(tmp)
            native = stage / native_name
            build(args.go, root / "backend", native, head, env)
            if checked([str(native), "version"], env=env, timeout=5) != head:
                raise PreflightError("embedded executable version does not equal git HEAD")
            catalog = read_json(checked([str(native), "catalog"], env=env, timeout=5))
            platforms = check_catalog(catalog)
            loopback = smoke(native, catalog, env, args.startup_timeout)
            refusals = check_refusals(native, available_port(), env)
            artifacts: list[Path] = [native]
            if args.linux_amd64:
                linux = stage / "dokitool-go-linux-amd64"
                cross_env = env.copy()
                cross_env.update({"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"})
                build(args.go, root / "backend", linux, head, cross_env)
                artifacts.append(linux)
            report = {
                "schema": "dokitool-go-offline-release-preflight-v1",
                "checked_at_utc": datetime.now(timezone.utc).isoformat(),
                "commit": head,
                "git_clean": clean,
                "go_version": go_toolchain,
                "native_host": {"system": platform.system(), "machine": platform.machine()},
                "platforms": sorted(platforms),
                "native_cli_version": head,
                "native_http": loopback,
                "refused_configs": refusals,
                "artifacts": [
                    {**digest(item), "native_smoke_tested": item == native}
                    for item in artifacts
                ],
                "production_approved": False,
                "limits": [
                    "No real platform request, browser save, DNS check, media fetch or production traffic",
                    "No PHP or Caddy/FPM rollback rehearsal",
                    "Linux cross-build is not Linux runtime verification" if args.linux_amd64
                    else "Native host binary is not a Linux production build",
                ],
            }
            if target:
                target.mkdir(parents=True, exist_ok=True)
                for item in artifacts:
                    temporary = target / (item.name + ".partial")
                    shutil.copyfile(item, temporary)
                    os.replace(temporary, target / item.name)
                receipt = target / "preflight-manifest.json"
                temporary = target / "preflight-manifest.json.partial"
                temporary.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
                os.replace(temporary, receipt)
            print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0
    except (PreflightError, OSError, ValueError) as exc:
        print(f"PRECHECK_FAILED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
