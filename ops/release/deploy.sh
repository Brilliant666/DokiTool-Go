#!/usr/bin/env bash
# Method B Production Release Deployment Script
# Usage: ops/release/deploy.sh <40-char-commit-sha>
#
# Security & Operational Invariants:
# - Accepts ONLY a full 40-character hexadecimal commit SHA (no branch names or 'latest').
# - Verifies BOTH release-manifest.json SHA-256 digests AND GitHub build provenance attestations
#   before modifying any release directory, symlink, or systemd drop-in.
# - Never reads, prints, or writes secret/token values or environment files.
# - Preserves previous hashed frontend assets (/assets/*) across releases and never unregisters Service Workers.
# - Automatically rolls back systemd ExecStart drop-in and frontend symlink if any smoke check fails.
# - Retains the 5 most recent releases.

set -euo pipefail
umask 022

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <40-char-commit-sha>" >&2
  exit 2
fi

TARGET_SHA="$1"
if [[ ! "$TARGET_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Error: target argument must be an exact 40-character lowercase hexadecimal commit SHA (got: '$TARGET_SHA')" >&2
  exit 2
fi

SHORT_SHA="${TARGET_SHA:0:7}"
REPO="${DOKITOOL_GITHUB_REPO:-Brilliant666/DokiTool-Go}"
RELEASE_TAG="${DOKITOOL_RELEASE_TAG:-build-${SHORT_SHA}}"

OPT_ROOT="${DOKITOOL_OPT_ROOT:-/opt/dokitool}"
SNAP_ROOT="${DOKITOOL_SNAP_ROOT:-/srv/snap}"
BACKEND_RELEASES_DIR="${OPT_ROOT}/releases"
FRONTEND_RELEASES_DIR="${SNAP_ROOT}/releases"
FRONTEND_CURRENT_LINK="${DOKITOOL_FRONTEND_LINK:-${SNAP_ROOT}/current}"

SERVICE_NAME="${DOKITOOL_SERVICE_NAME:-dokitool-go.service}"
SYSTEMD_DROPIN_DIR="${DOKITOOL_SYSTEMD_DROPIN_DIR:-/etc/systemd/system/${SERVICE_NAME}.d}"
DROPIN_FILE="${SYSTEMD_DROPIN_DIR}/${DOKITOOL_DROPIN_FILENAME:-90-release-exec.conf}"
EXEC_ARGS="${DOKITOOL_EXEC_ARGS:-serve -listen 127.0.0.1:9082 -client-identity caddy -xhs-page-transport chrome131}"

API_BASE_URL="${DOKITOOL_API_BASE_URL:-http://127.0.0.1:9082}"
MIN_PLATFORMS="${DOKITOOL_MIN_PLATFORMS:-9}"
EXPECTED_PLATFORMS="${DOKITOOL_EXPECTED_PLATFORMS:-}"
KEEP_RELEASES="${DOKITOOL_KEEP_RELEASES:-5}"
STOP_TIMEOUT_SEC="${DOKITOOL_STOP_TIMEOUT_SEC:-315}"

GH_BIN="${DOKITOOL_GH_BIN:-gh}"
SYSTEMCTL_BIN="${DOKITOOL_SYSTEMCTL_BIN:-systemctl}"
CURL_BIN="${DOKITOOL_CURL_BIN:-curl}"
PYTHON_BIN="${DOKITOOL_PYTHON_BIN:-python3}"

STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/dokitool-deploy-stage.XXXXXXXX")"
cleanup_stage() {
  rm -rf "$STAGE_DIR"
}
trap cleanup_stage EXIT INT TERM

log() {
  printf '[deploy %s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

atomic_symlink() {
  local target="$1"
  local link_path="$2"
  local tmp_link="${link_path}.tmp.$$"
  ln -sfn "$target" "$tmp_link"
  "$PYTHON_BIN" -c 'import os, sys; os.replace(sys.argv[1], sys.argv[2])' "$tmp_link" "$link_path"
}

merge_missing_files() {
  local src_dir="$1"
  local dst_dir="$2"
  [ -d "$src_dir" ] || return 0
  mkdir -p "$dst_dir"
  "$PYTHON_BIN" - "$src_dir" "$dst_dir" <<'PY'
import shutil
import sys
from pathlib import Path

src = Path(sys.argv[1])
dst = Path(sys.argv[2])
for item in src.rglob("*"):
    if not item.is_file():
        continue
    rel = item.relative_to(src)
    target = dst / rel
    if not target.exists():
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(item, target)
PY
}

# ---------------------------------------------------------------------------
# Phase 1: Download Release Assets into Isolated Staging Directory
# ---------------------------------------------------------------------------
log "Downloading release '${RELEASE_TAG}' for SHA ${TARGET_SHA} from ${REPO}..."
if [ -n "${DOKITOOL_LOCAL_ARTIFACT_DIR:-}" ]; then
  for asset in dokitool shanghai-resolver frontend-dist.tar.gz release-manifest.json; do
    cp "${DOKITOOL_LOCAL_ARTIFACT_DIR}/${asset}" "${STAGE_DIR}/${asset}"
  done
else
  "$GH_BIN" release download "$RELEASE_TAG" \
    --repo "$REPO" \
    --dir "$STAGE_DIR" \
    --pattern "dokitool" \
    --pattern "shanghai-resolver" \
    --pattern "frontend-dist.tar.gz" \
    --pattern "release-manifest.json"
fi

for asset in dokitool shanghai-resolver frontend-dist.tar.gz release-manifest.json; do
  if [ ! -s "${STAGE_DIR}/${asset}" ]; then
    echo "Error: missing or empty release asset '${asset}'" >&2
    exit 1
  fi
done

# ---------------------------------------------------------------------------
# Phase 2: Gate 1 — Verify release-manifest.json and SHA-256 Digests
# ---------------------------------------------------------------------------
log "Verifying release-manifest.json and artifact SHA-256 digests..."
mkdir -p "${STAGE_DIR}/frontend-extracted"
tar -xzf "${STAGE_DIR}/frontend-dist.tar.gz" -C "${STAGE_DIR}/frontend-extracted"

STAGE_DIR_ENV="$STAGE_DIR" TARGET_SHA_ENV="$TARGET_SHA" RELEASE_TAG_ENV="$RELEASE_TAG" "$PYTHON_BIN" - <<'PY'
import hashlib
import json
import os
import sys
from pathlib import Path

stage = Path(os.environ["STAGE_DIR_ENV"])
target_sha = os.environ["TARGET_SHA_ENV"]
release_tag = os.environ["RELEASE_TAG_ENV"]
manifest = json.loads((stage / "release-manifest.json").read_text(encoding="utf-8"))

if manifest.get("source_sha") != target_sha:
    print(f"Error: manifest source_sha ({manifest.get('source_sha')}) != target SHA ({target_sha})", file=sys.stderr)
    sys.exit(1)

if manifest.get("release_tag") != release_tag:
    print(f"Error: manifest release_tag ({manifest.get('release_tag')}) != expected release_tag ({release_tag})", file=sys.stderr)
    sys.exit(1)

def sha256_path(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()

artifacts = manifest.get("artifacts", {})
for name in ("dokitool", "shanghai-resolver", "frontend-dist.tar.gz"):
    info = artifacts.get(name)
    if not isinstance(info, dict):
        print(f"Error: manifest missing artifact entry for '{name}'", file=sys.stderr)
        sys.exit(1)
    fpath = stage / name
    actual_sha = sha256_path(fpath)
    expected_sha = info.get("sha256", "")
    if actual_sha != expected_sha:
        print(f"Error: SHA-256 mismatch for {name}: got {actual_sha}, want {expected_sha}", file=sys.stderr)
        sys.exit(1)
    expected_size = info.get("size_bytes")
    if isinstance(expected_size, int) and fpath.stat().st_size != expected_size:
        print(f"Error: size mismatch for {name}: got {fpath.stat().st_size}, want {expected_size}", file=sys.stderr)
        sys.exit(1)

dist_root = stage / "frontend-extracted"
manifest_dist = manifest.get("frontend_dist_files", {})
if not isinstance(manifest_dist, dict) or not manifest_dist:
    print("Error: manifest frontend_dist_files is empty or invalid", file=sys.stderr)
    sys.exit(1)

actual_dist = {}
for fpath in sorted(p for p in dist_root.rglob("*") if p.is_file()):
    rel = fpath.relative_to(dist_root).as_posix()
    actual_dist[rel] = sha256_path(fpath)

if actual_dist != manifest_dist:
    print("Error: extracted frontend/dist files do not match release-manifest.json", file=sys.stderr)
    sys.exit(1)
PY

EXPECTED_DOKITOOL_SHA256="$(sha256_file "${STAGE_DIR}/dokitool")"
log "Manifest and per-file SHA-256 verification passed (dokitool=${EXPECTED_DOKITOOL_SHA256})."

# ---------------------------------------------------------------------------
# Phase 3: Gate 2 — Verify GitHub Build Provenance Attestations
# ---------------------------------------------------------------------------
SIGNER_WORKFLOW="${DOKITOOL_SIGNER_WORKFLOW:-${REPO}/.github/workflows/release-build.yml}"
log "Verifying GitHub build provenance attestations..."
for asset in dokitool shanghai-resolver frontend-dist.tar.gz release-manifest.json; do
  "$GH_BIN" attestation verify "${STAGE_DIR}/${asset}" \
    --repo "$REPO" \
    --signer-workflow "$SIGNER_WORKFLOW" >/dev/null
done
log "All 4 release artifacts passed gh attestation verify."

# ---------------------------------------------------------------------------
# Phase 4: Record Pre-Deploy State for Atomic Rollback
# ---------------------------------------------------------------------------
mkdir -p "$BACKEND_RELEASES_DIR" "$FRONTEND_RELEASES_DIR" "$SYSTEMD_DROPIN_DIR"

PREV_FRONTEND_TARGET=""
if [ -L "$FRONTEND_CURRENT_LINK" ]; then
  PREV_FRONTEND_TARGET="$(readlink "$FRONTEND_CURRENT_LINK")"
fi

HAD_PREV_DROPIN=0
PREV_DROPIN_BACKUP="${STAGE_DIR}/prev-dropin.conf"
if [ -f "$DROPIN_FILE" ]; then
  HAD_PREV_DROPIN=1
  cp -p "$DROPIN_FILE" "$PREV_DROPIN_BACKUP"
fi

stop_and_drain_service() {
  if "$SYSTEMCTL_BIN" is-active --quiet "$SERVICE_NAME"; then
    local old_pid
    old_pid="$("$SYSTEMCTL_BIN" show "$SERVICE_NAME" -p MainPID --value 2>/dev/null || echo 0)"
    if command -v timeout >/dev/null 2>&1; then
      timeout "$STOP_TIMEOUT_SEC" "$SYSTEMCTL_BIN" stop "$SERVICE_NAME"
    else
      "$SYSTEMCTL_BIN" stop "$SERVICE_NAME"
    fi
    local waited=0
    while [ "$old_pid" -gt 0 ] 2>/dev/null && kill -0 "$old_pid" 2>/dev/null; do
      if [ "$waited" -ge "$STOP_TIMEOUT_SEC" ]; then
        echo "Error: old process PID ${old_pid} did not exit within ${STOP_TIMEOUT_SEC}s" >&2
        return 1
      fi
      sleep 1
      waited=$((waited + 1))
    done
  fi
}

rollback_to_previous() {
  log "Rolling back to pre-deploy configuration and frontend symlink..."
  if [ "$HAD_PREV_DROPIN" -eq 1 ]; then
    cp -p "$PREV_DROPIN_BACKUP" "$DROPIN_FILE"
  else
    rm -f "$DROPIN_FILE"
  fi
  if [ -n "$PREV_FRONTEND_TARGET" ]; then
    atomic_symlink "$PREV_FRONTEND_TARGET" "$FRONTEND_CURRENT_LINK"
  fi
  "$SYSTEMCTL_BIN" daemon-reload || true
  stop_and_drain_service || true
  "$SYSTEMCTL_BIN" start "$SERVICE_NAME" || true
  log "Rollback to previous release completed."
}

# ---------------------------------------------------------------------------
# Phase 5: Stage Backend & Frontend Releases (Preserving Previous Hashed Assets)
# ---------------------------------------------------------------------------
BACKEND_TARGET_DIR="${BACKEND_RELEASES_DIR}/${TARGET_SHA}"
FRONTEND_TARGET_DIR="${FRONTEND_RELEASES_DIR}/${TARGET_SHA}"

mkdir -p "$BACKEND_TARGET_DIR" "$FRONTEND_TARGET_DIR"
install -m 0755 "${STAGE_DIR}/dokitool" "${BACKEND_TARGET_DIR}/dokitool"
install -m 0755 "${STAGE_DIR}/shanghai-resolver" "${BACKEND_TARGET_DIR}/shanghai-resolver"
install -m 0644 "${STAGE_DIR}/release-manifest.json" "${BACKEND_TARGET_DIR}/release-manifest.json"

rm -rf "${FRONTEND_TARGET_DIR}.new.$$"
mkdir -p "${FRONTEND_TARGET_DIR}.new.$$"
cp -a "${STAGE_DIR}/frontend-extracted/." "${FRONTEND_TARGET_DIR}.new.$$/"

if [ -n "$PREV_FRONTEND_TARGET" ] && [ -d "${PREV_FRONTEND_TARGET}/assets" ]; then
  merge_missing_files "${PREV_FRONTEND_TARGET}/assets" "${FRONTEND_TARGET_DIR}.new.$$/assets"
fi

rm -rf "$FRONTEND_TARGET_DIR"
mv "${FRONTEND_TARGET_DIR}.new.$$" "$FRONTEND_TARGET_DIR"
touch "$BACKEND_TARGET_DIR" "$FRONTEND_TARGET_DIR"

# ---------------------------------------------------------------------------
# Phase 6: Write ExecStart-Only Systemd Drop-in, Drain/Restart, and Switch Symlink
# ---------------------------------------------------------------------------
cat > "${DROPIN_FILE}.tmp.$$" <<EOF
[Service]
ExecStart=
ExecStart=${BACKEND_TARGET_DIR}/dokitool ${EXEC_ARGS}
EOF
chmod 0644 "${DROPIN_FILE}.tmp.$$"
mv -f "${DROPIN_FILE}.tmp.$$" "$DROPIN_FILE"

log "Reloading systemd, draining existing requests, and starting ${SERVICE_NAME}..."
if ! "$SYSTEMCTL_BIN" daemon-reload || ! stop_and_drain_service || ! "$SYSTEMCTL_BIN" start "$SERVICE_NAME"; then
  echo "Error: failed to restart ${SERVICE_NAME} on ${TARGET_SHA}" >&2
  rollback_to_previous
  exit 1
fi

atomic_symlink "$FRONTEND_TARGET_DIR" "$FRONTEND_CURRENT_LINK"

# ---------------------------------------------------------------------------
# Phase 7: Post-Switch Smoke Checks (Auto-Rollback on Any Failure)
# ---------------------------------------------------------------------------
run_smoke_checks() {
  local ver_out
  ver_out="$("${BACKEND_TARGET_DIR}/dokitool" version 2>/dev/null || true)"
  if [ "$ver_out" != "$TARGET_SHA" ]; then
    echo "Smoke check failed: dokitool version returned '${ver_out}', want '${TARGET_SHA}'" >&2
    return 1
  fi

  local installed_sha
  installed_sha="$(sha256_file "${BACKEND_TARGET_DIR}/dokitool")"
  if [ "$installed_sha" != "$EXPECTED_DOKITOOL_SHA256" ]; then
    echo "Smoke check failed: installed dokitool SHA-256 (${installed_sha}) != manifest (${EXPECTED_DOKITOOL_SHA256})" >&2
    return 1
  fi

  if ! "$SYSTEMCTL_BIN" is-active --quiet "$SERVICE_NAME"; then
    echo "Smoke check failed: ${SERVICE_NAME} is not active" >&2
    return 1
  fi

  local platforms_json=""
  local attempt
  for attempt in $(seq 1 25); do
    if platforms_json="$("$CURL_BIN" -fsS --max-time 3 "${API_BASE_URL}/api/platforms?schema_version=1.0" 2>/dev/null)"; then
      break
    fi
    sleep 0.2
  done
  if [ -z "$platforms_json" ]; then
    echo "Smoke check failed: GET ${API_BASE_URL}/api/platforms?schema_version=1.0 did not respond with 200" >&2
    return 1
  fi

  if ! PLATFORMS_JSON_ENV="$platforms_json" MIN_PLATFORMS_ENV="$MIN_PLATFORMS" EXPECTED_PLATFORMS_ENV="$EXPECTED_PLATFORMS" "$PYTHON_BIN" - <<'PY'; then
import json
import os
import sys

payload = json.loads(os.environ["PLATFORMS_JSON_ENV"])
if payload.get("code") != "OK":
    print(f"Smoke check failed: /api/platforms code={payload.get('code')}", file=sys.stderr)
    sys.exit(1)
platforms = payload.get("data", {}).get("platforms", [])
count = len(platforms)
min_p = int(os.environ["MIN_PLATFORMS_ENV"])
exp_p = os.environ["EXPECTED_PLATFORMS_ENV"].strip()
if exp_p:
    if count != int(exp_p):
        print(f"Smoke check failed: platform count {count} != expected {exp_p}", file=sys.stderr)
        sys.exit(1)
elif count < min_p:
    print(f"Smoke check failed: platform count {count} < minimum {min_p}", file=sys.stderr)
    sys.exit(1)
PY
    return 1
  fi
  return 0
}

log "Running post-switch smoke checks..."
if ! run_smoke_checks; then
  echo "Error: post-switch smoke checks failed; triggering automatic rollback" >&2
  rollback_to_previous
  exit 1
fi
log "All post-switch smoke checks passed."

# ---------------------------------------------------------------------------
# Phase 8: Retain Only the Most Recent 5 Releases
# ---------------------------------------------------------------------------
prune_old_releases() {
  local base_dir="$1"
  local keep_sha_1="$2"
  local keep_sha_2="$3"
  [ -d "$base_dir" ] || return 0
  "$PYTHON_BIN" - "$base_dir" "$KEEP_RELEASES" "$keep_sha_1" "$keep_sha_2" <<'PY'
import shutil
import sys
from pathlib import Path

base = Path(sys.argv[1])
keep_n = int(sys.argv[2])
protected = {s for s in sys.argv[3:] if s}

dirs = [p for p in base.iterdir() if p.is_dir() and not p.is_symlink()]
dirs.sort(key=lambda p: (p.name in protected, p.stat().st_mtime), reverse=True)

for idx, d in enumerate(dirs):
    if idx < keep_n or d.name in protected:
        continue
    shutil.rmtree(d, ignore_errors=True)
PY
}

PREV_SHA_NAME=""
if [ -n "$PREV_FRONTEND_TARGET" ]; then
  PREV_SHA_NAME="$(basename "$PREV_FRONTEND_TARGET")"
fi
prune_old_releases "$BACKEND_RELEASES_DIR" "$TARGET_SHA" "$PREV_SHA_NAME"
prune_old_releases "$FRONTEND_RELEASES_DIR" "$TARGET_SHA" "$PREV_SHA_NAME"

log "Deployment of ${TARGET_SHA} succeeded."
