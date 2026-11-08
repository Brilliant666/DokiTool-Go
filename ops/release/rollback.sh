#!/usr/bin/env bash
# Method B Production Release Rollback Script
# Usage: ops/release/rollback.sh <40-char-commit-sha>
#
# Security & Operational Invariants:
# - Accepts ONLY a full 40-character hexadecimal commit SHA already installed on disk.
# - Verifies the installed binary SHA-256 against its stored release-manifest.json before switching.
# - Merges current hashed frontend assets (/assets/*) into the target release so open tabs never 404.
# - Never reads, prints, or writes secret/token values or environment files.

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

OPT_ROOT="${DOKITOOL_OPT_ROOT:-/opt/dokitool}"
SNAP_ROOT="${DOKITOOL_SNAP_ROOT:-/srv/snap}"
BACKEND_TARGET_DIR="${OPT_ROOT}/releases/${TARGET_SHA}"
FRONTEND_TARGET_DIR="${SNAP_ROOT}/releases/${TARGET_SHA}"
FRONTEND_CURRENT_LINK="${DOKITOOL_FRONTEND_LINK:-${SNAP_ROOT}/current}"

SERVICE_NAME="${DOKITOOL_SERVICE_NAME:-dokitool-go.service}"
SYSTEMD_DROPIN_DIR="${DOKITOOL_SYSTEMD_DROPIN_DIR:-/etc/systemd/system/${SERVICE_NAME}.d}"
DROPIN_FILE="${SYSTEMD_DROPIN_DIR}/${DOKITOOL_DROPIN_FILENAME:-90-release-exec.conf}"
EXEC_ARGS="${DOKITOOL_EXEC_ARGS:-serve -listen 127.0.0.1:9082 -client-identity caddy -xhs-page-transport chrome131}"

API_BASE_URL="${DOKITOOL_API_BASE_URL:-http://127.0.0.1:9082}"
MIN_PLATFORMS="${DOKITOOL_MIN_PLATFORMS:-9}"
EXPECTED_PLATFORMS="${DOKITOOL_EXPECTED_PLATFORMS:-}"
STOP_TIMEOUT_SEC="${DOKITOOL_STOP_TIMEOUT_SEC:-315}"

SYSTEMCTL_BIN="${DOKITOOL_SYSTEMCTL_BIN:-systemctl}"
CURL_BIN="${DOKITOOL_CURL_BIN:-curl}"
PYTHON_BIN="${DOKITOOL_PYTHON_BIN:-python3}"

log() {
  printf '[rollback %s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
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

if [ ! -x "${BACKEND_TARGET_DIR}/dokitool" ] || [ ! -f "${BACKEND_TARGET_DIR}/release-manifest.json" ]; then
  echo "Error: backend release directory ${BACKEND_TARGET_DIR} is missing dokitool or release-manifest.json" >&2
  exit 1
fi
if [ ! -d "$FRONTEND_TARGET_DIR" ] || [ ! -f "${FRONTEND_TARGET_DIR}/index.html" ]; then
  echo "Error: frontend release directory ${FRONTEND_TARGET_DIR} is missing index.html" >&2
  exit 1
fi

EXPECTED_DOKITOOL_SHA256="$(
  MANIFEST_PATH="${BACKEND_TARGET_DIR}/release-manifest.json" TARGET_SHA_ENV="$TARGET_SHA" "$PYTHON_BIN" - <<'PY'
import json
import os
import sys
from pathlib import Path

m = json.loads(Path(os.environ["MANIFEST_PATH"]).read_text(encoding="utf-8"))
if m.get("source_sha") != os.environ["TARGET_SHA_ENV"]:
    sys.exit(1)
sha = m.get("artifacts", {}).get("dokitool", {}).get("sha256", "")
if not sha:
    sys.exit(1)
print(sha)
PY
)"

ACTUAL_DOKITOOL_SHA256="$(sha256_file "${BACKEND_TARGET_DIR}/dokitool")"
if [ "$ACTUAL_DOKITOOL_SHA256" != "$EXPECTED_DOKITOOL_SHA256" ]; then
  echo "Error: binary SHA-256 mismatch in ${BACKEND_TARGET_DIR}/dokitool (${ACTUAL_DOKITOOL_SHA256} != ${EXPECTED_DOKITOOL_SHA256})" >&2
  exit 1
fi

if [ -L "$FRONTEND_CURRENT_LINK" ]; then
  CUR_FRONTEND_TARGET="$(readlink "$FRONTEND_CURRENT_LINK")"
  if [ -n "$CUR_FRONTEND_TARGET" ] && [ "$CUR_FRONTEND_TARGET" != "$FRONTEND_TARGET_DIR" ] && [ -d "${CUR_FRONTEND_TARGET}/assets" ]; then
    merge_missing_files "${CUR_FRONTEND_TARGET}/assets" "${FRONTEND_TARGET_DIR}/assets"
  fi
fi

mkdir -p "$SYSTEMD_DROPIN_DIR"
cat > "${DROPIN_FILE}.tmp.$$" <<EOF
[Service]
ExecStart=
ExecStart=${BACKEND_TARGET_DIR}/dokitool ${EXEC_ARGS}
EOF
chmod 0644 "${DROPIN_FILE}.tmp.$$"
mv -f "${DROPIN_FILE}.tmp.$$" "$DROPIN_FILE"

log "Reloading systemd, draining active requests, and starting ${SERVICE_NAME} on ${TARGET_SHA}..."
"$SYSTEMCTL_BIN" daemon-reload
if "$SYSTEMCTL_BIN" is-active --quiet "$SERVICE_NAME"; then
  OLD_PID="$("$SYSTEMCTL_BIN" show "$SERVICE_NAME" -p MainPID --value 2>/dev/null || echo 0)"
  if command -v timeout >/dev/null 2>&1; then
    timeout "$STOP_TIMEOUT_SEC" "$SYSTEMCTL_BIN" stop "$SERVICE_NAME"
  else
    "$SYSTEMCTL_BIN" stop "$SERVICE_NAME"
  fi
  WAITED=0
  while [ "$OLD_PID" -gt 0 ] 2>/dev/null && kill -0 "$OLD_PID" 2>/dev/null; do
    if [ "$WAITED" -ge "$STOP_TIMEOUT_SEC" ]; then
      echo "Error: old process PID ${OLD_PID} did not exit within ${STOP_TIMEOUT_SEC}s" >&2
      exit 1
    fi
    sleep 1
    WAITED=$((WAITED + 1))
  done
fi
"$SYSTEMCTL_BIN" start "$SERVICE_NAME"

atomic_symlink "$FRONTEND_TARGET_DIR" "$FRONTEND_CURRENT_LINK"
touch "$BACKEND_TARGET_DIR" "$FRONTEND_TARGET_DIR"

VER_OUT="$("${BACKEND_TARGET_DIR}/dokitool" version)"
if [ "$VER_OUT" != "$TARGET_SHA" ]; then
  echo "Error: rollback smoke check failed: dokitool version=${VER_OUT}, want ${TARGET_SHA}" >&2
  exit 1
fi

PLATFORMS_JSON=""
for _ in $(seq 1 25); do
  if PLATFORMS_JSON="$("$CURL_BIN" -fsS --max-time 3 "${API_BASE_URL}/api/platforms?schema_version=1.0" 2>/dev/null)"; then
    break
  fi
  sleep 0.2
done
if [ -z "$PLATFORMS_JSON" ]; then
  echo "Error: rollback smoke check failed: ${API_BASE_URL}/api/platforms?schema_version=1.0 unreachable" >&2
  exit 1
fi

PLATFORMS_JSON_ENV="$PLATFORMS_JSON" MIN_PLATFORMS_ENV="$MIN_PLATFORMS" EXPECTED_PLATFORMS_ENV="$EXPECTED_PLATFORMS" "$PYTHON_BIN" - <<'PY'
import json
import os
import sys

payload = json.loads(os.environ["PLATFORMS_JSON_ENV"])
if payload.get("code") != "OK":
    sys.exit(1)
count = len(payload.get("data", {}).get("platforms", []))
exp_p = os.environ["EXPECTED_PLATFORMS_ENV"].strip()
if exp_p:
    if count != int(exp_p):
        sys.exit(1)
elif count < int(os.environ["MIN_PLATFORMS_ENV"]):
    sys.exit(1)
PY

log "Rollback to ${TARGET_SHA} succeeded and passed smoke checks."
