#!/usr/bin/env bash
# Method B Production Release Rollback Script
# Usage: ops/release/rollback.sh <40-char-commit-sha>
#
# Security & Operational Invariants:
# - Accepts ONLY a full 40-character hexadecimal commit SHA already installed on disk.
# - Reads explicit paths, arguments, and URLs from /etc/dokitool/release.conf (0644, root-owned, no secrets)
#   with zero fallback defaults in the script.
# - Acquires an exclusive flock shared with ops/release/deploy.sh to prevent concurrent release operations.
# - Compares active systemd ExecStart arguments against EXEC_ARGS before making any changes.
# - Verifies the installed binary SHA-256 against release-manifest.json (or verifies existing systemd drop-in
#   reference and binary version for pre-Method-B releases) before switching.
# - Merges current hashed frontend assets (/assets/*) into the target release so open tabs never 404.
# - Runs full post-switch smoke checks (binary version/SHA-256, systemd active state, local listener,
#   public /api/platforms, public homepage + all referenced assets/*, and preserved hashed chunk).
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

RELEASE_CONF="${DOKITOOL_RELEASE_CONF:-/etc/dokitool/release.conf}"
SERVICE_NAME="${DOKITOOL_SERVICE_NAME:-dokitool-go.service}"
SYSTEMD_DROPIN_DIR="${DOKITOOL_SYSTEMD_DROPIN_DIR:-/etc/systemd/system/${SERVICE_NAME}.d}"
DROPIN_FILE="${SYSTEMD_DROPIN_DIR}/${DOKITOOL_DROPIN_FILENAME:-90-release-exec.conf}"

MIN_PLATFORMS="${DOKITOOL_MIN_PLATFORMS:-9}"
EXPECTED_PLATFORMS="${DOKITOOL_EXPECTED_PLATFORMS:-}"
STOP_TIMEOUT_SEC="${DOKITOOL_STOP_TIMEOUT_SEC:-315}"

SYSTEMCTL_BIN="${DOKITOOL_SYSTEMCTL_BIN:-systemctl}"
CURL_BIN="${DOKITOOL_CURL_BIN:-curl}"
PYTHON_BIN="${DOKITOOL_PYTHON_BIN:-python3}"

STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/dokitool-rollback-stage.XXXXXXXX")"
cleanup_stage() {
  flock -u 9 2>/dev/null || true
  rm -rf "$STAGE_DIR"
}
trap cleanup_stage EXIT INT TERM

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

# ---------------------------------------------------------------------------
# Phase 0: Load & Validate /etc/dokitool/release.conf and Active Service State
# ---------------------------------------------------------------------------
RELEASE_CONF_ENV="$RELEASE_CONF" "$PYTHON_BIN" - <<'PY'
import os
import stat
import sys
from pathlib import Path

conf_path = Path(os.environ["RELEASE_CONF_ENV"])
if not conf_path.exists() or conf_path.is_symlink() or not conf_path.is_file():
    print(f"Error: release configuration file {conf_path} is missing or not a regular file", file=sys.stderr)
    sys.exit(1)

st = conf_path.stat()
mode = stat.S_IMODE(st.st_mode)
if mode != 0o644:
    print(f"Error: {conf_path} must have mode 0644 (got {oct(mode)})", file=sys.stderr)
    sys.exit(1)

euid = os.geteuid()
if st.st_uid not in (0, euid):
    print(f"Error: {conf_path} must be owned by root (uid 0), got uid {st.st_uid}", file=sys.stderr)
    sys.exit(1)

required_keys = {
    "RELEASES_ROOT",
    "FRONTEND_SYMLINK",
    "EXEC_ARGS",
    "LOCAL_API_URL",
    "PUBLIC_SITE_URL",
}
found = {}
for lineno, raw_line in enumerate(conf_path.read_text(encoding="utf-8").splitlines(), start=1):
    line = raw_line.strip()
    if not line or line.startswith("#"):
        continue
    if "=" not in line:
        print(f"Error: invalid line {lineno} in {conf_path}", file=sys.stderr)
        sys.exit(1)
    k, v = line.split("=", 1)
    k = k.strip()
    v = v.strip()
    if k not in required_keys:
        print(f"Error: unsupported or disallowed key '{k}' in {conf_path}", file=sys.stderr)
        sys.exit(1)
    found[k] = v

missing = sorted(k for k in required_keys if not found.get(k))
if missing:
    print(f"Error: missing required keys in {conf_path}: {', '.join(missing)}", file=sys.stderr)
    sys.exit(1)
PY

RELEASES_ROOT=""
FRONTEND_SYMLINK=""
EXEC_ARGS=""
LOCAL_API_URL=""
PUBLIC_SITE_URL=""
while IFS= read -r line || [ -n "$line" ]; do
  line="${line#"${line%%[![:space:]]*}"}"
  case "$line" in
    ""|\#*) continue ;;
  esac
  key="${line%%=*}"
  val="${line#*=}"
  key="${key%"${key##*[![:space:]]}"}"
  val="${val#"${val%%[![:space:]]*}"}"
  val="${val%"${val##*[![:space:]]}"}"
  case "$key" in
    RELEASES_ROOT) RELEASES_ROOT="$val" ;;
    FRONTEND_SYMLINK) FRONTEND_SYMLINK="$val" ;;
    EXEC_ARGS) EXEC_ARGS="$val" ;;
    LOCAL_API_URL) LOCAL_API_URL="$val" ;;
    PUBLIC_SITE_URL) PUBLIC_SITE_URL="$val" ;;
  esac
done < "$RELEASE_CONF"

LOCK_FILE="${DOKITOOL_LOCK_FILE:-${RELEASES_ROOT}/.release.lock}"
mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "Error: another deploy or rollback operation is already holding lock ${LOCK_FILE}" >&2
  exit 1
fi

RAW_EXEC_START="$("$SYSTEMCTL_BIN" show -p ExecStart "$SERVICE_NAME" 2>/dev/null || true)"
if [ -z "$RAW_EXEC_START" ]; then
  echo "Error: unable to read ExecStart from systemctl show -p ExecStart ${SERVICE_NAME}" >&2
  exit 1
fi

ACTIVE_EXEC_PATH="$(
  RAW_EXEC_ENV="$RAW_EXEC_START" EXPECTED_ARGS_ENV="$EXEC_ARGS" CONF_PATH_ENV="$RELEASE_CONF" "$PYTHON_BIN" - <<'PY'
import os
import re
import sys

raw = os.environ["RAW_EXEC_ENV"].strip()
expected_args = os.environ["EXPECTED_ARGS_ENV"].strip()
conf_path = os.environ["CONF_PATH_ENV"]

m = re.search(r"argv\[\]=([^\s;]+)(?:\s+([^;]*?))?\s*;", raw)
if m:
    exec_path = m.group(1).strip()
    exec_args = (m.group(2) or "").strip()
else:
    val = raw
    if val.startswith("ExecStart="):
        val = val[len("ExecStart="):].strip()
    parts = val.split(None, 1)
    exec_path = parts[0] if parts else ""
    exec_args = parts[1].strip() if len(parts) > 1 else ""

if not exec_path:
    print(f"Error: could not parse active binary path from systemctl ExecStart: {raw}", file=sys.stderr)
    sys.exit(1)

if exec_args != expected_args:
    print(f"Error: active systemd ExecStart arguments do not match EXEC_ARGS in {conf_path}:", file=sys.stderr)
    print(f"  - active (systemctl): {exec_args}", file=sys.stderr)
    print(f"  + release.conf:       {expected_args}", file=sys.stderr)
    sys.exit(1)

print(exec_path)
PY
)"

RELEASE_TARGET_DIR="${RELEASES_ROOT}/${TARGET_SHA}"
BACKEND_BIN_DIR="${RELEASE_TARGET_DIR}/bin"
FRONTEND_TARGET_DIR="${RELEASE_TARGET_DIR}/frontend/dist"

CUR_FRONTEND_TARGET=""
if [ -L "$FRONTEND_SYMLINK" ]; then
  CUR_FRONTEND_TARGET="$(readlink "$FRONTEND_SYMLINK")"
fi

if [ "$ACTIVE_EXEC_PATH" = "${BACKEND_BIN_DIR}/dokitool-go" ] && [ "$CUR_FRONTEND_TARGET" = "$FRONTEND_TARGET_DIR" ]; then
  echo "Error: target SHA ${TARGET_SHA} is already the currently active release" >&2
  exit 1
fi

if [ ! -x "${BACKEND_BIN_DIR}/dokitool-go" ]; then
  echo "Error: backend release binary ${BACKEND_BIN_DIR}/dokitool-go is missing or not executable" >&2
  exit 1
fi
if [ ! -d "$FRONTEND_TARGET_DIR" ] || [ ! -f "${FRONTEND_TARGET_DIR}/index.html" ]; then
  echo "Error: frontend release directory ${FRONTEND_TARGET_DIR} is missing index.html" >&2
  exit 1
fi

ACTUAL_DOKITOOL_SHA256="$(sha256_file "${BACKEND_BIN_DIR}/dokitool-go")"
if [ -f "${RELEASE_TARGET_DIR}/release-manifest.json" ]; then
  EXPECTED_DOKITOOL_SHA256="$(
    MANIFEST_PATH="${RELEASE_TARGET_DIR}/release-manifest.json" TARGET_SHA_ENV="$TARGET_SHA" "$PYTHON_BIN" - <<'PY'
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
  if [ "$ACTUAL_DOKITOOL_SHA256" != "$EXPECTED_DOKITOOL_SHA256" ]; then
    echo "Error: binary SHA-256 mismatch in ${BACKEND_BIN_DIR}/dokitool-go (${ACTUAL_DOKITOOL_SHA256} != ${EXPECTED_DOKITOOL_SHA256})" >&2
    exit 1
  fi
elif [ -f "${RELEASE_TARGET_DIR}/.method-b-release" ]; then
  echo "Error: Method B release ${RELEASE_TARGET_DIR} is missing release-manifest.json" >&2
  exit 1
else
  # Pre-Method-B release: require an existing systemd drop-in referencing this binary and matching version output
  if ! DROPIN_DIR_ENV="$SYSTEMD_DROPIN_DIR" BIN_PATH_ENV="${BACKEND_BIN_DIR}/dokitool-go" "$PYTHON_BIN" - <<'PY'; then
import os
import sys
from pathlib import Path

dropin_dir = Path(os.environ["DROPIN_DIR_ENV"])
bin_path = os.environ["BIN_PATH_ENV"]
if not dropin_dir.is_dir():
    sys.exit(1)
for conf in sorted(dropin_dir.glob("*.conf")):
    try:
        if bin_path in conf.read_text(encoding="utf-8", errors="ignore"):
            sys.exit(0)
    except OSError:
        pass
sys.exit(1)
PY
    echo "Error: ${RELEASE_TARGET_DIR} has no release-manifest.json and is not referenced by any drop-in in ${SYSTEMD_DROPIN_DIR}" >&2
    exit 1
  fi
  EXPECTED_DOKITOOL_SHA256="$ACTUAL_DOKITOOL_SHA256"
fi

VER_CHECK="$("${BACKEND_BIN_DIR}/dokitool-go" version 2>/dev/null || true)"
if [ "$VER_CHECK" != "$TARGET_SHA" ]; then
  echo "Error: ${BACKEND_BIN_DIR}/dokitool-go version returned '${VER_CHECK}', want '${TARGET_SHA}'" >&2
  exit 1
fi

HAD_PREV_DROPIN=0
PREV_DROPIN_BACKUP="${STAGE_DIR}/prev-dropin.conf"
if [ -f "$DROPIN_FILE" ]; then
  HAD_PREV_DROPIN=1
  cp -p "$DROPIN_FILE" "$PREV_DROPIN_BACKUP"
fi

PREV_ASSET_SAMPLE=""
if [ -n "$CUR_FRONTEND_TARGET" ] && [ "$CUR_FRONTEND_TARGET" != "$FRONTEND_TARGET_DIR" ] && [ -d "${CUR_FRONTEND_TARGET}/assets" ]; then
  PREV_ASSET_SAMPLE="$(
    CUR_ASSETS_ENV="${CUR_FRONTEND_TARGET}/assets" TARGET_ASSETS_ENV="${FRONTEND_TARGET_DIR}/assets" "$PYTHON_BIN" - <<'PY'
import os
from pathlib import Path

cur_root = Path(os.environ["CUR_ASSETS_ENV"])
target_root = Path(os.environ["TARGET_ASSETS_ENV"])
cur_files = sorted(p.relative_to(cur_root).as_posix() for p in cur_root.rglob("*") if p.is_file())
target_files = {p.relative_to(target_root).as_posix() for p in target_root.rglob("*") if p.is_file()} if target_root.is_dir() else set()

only_cur = [f for f in cur_files if f not in target_files]
if only_cur:
    print(only_cur[0])
elif cur_files:
    print(cur_files[0])
PY
  )"
  merge_missing_files "${CUR_FRONTEND_TARGET}/assets" "${FRONTEND_TARGET_DIR}/assets"
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

mkdir -p "$SYSTEMD_DROPIN_DIR"
cat > "${DROPIN_FILE}.tmp.$$" <<EOF
[Service]
ExecStart=
ExecStart=${BACKEND_BIN_DIR}/dokitool-go ${EXEC_ARGS}
EOF
chmod 0644 "${DROPIN_FILE}.tmp.$$"
mv -f "${DROPIN_FILE}.tmp.$$" "$DROPIN_FILE"

log "Reloading systemd, draining active requests, and starting ${SERVICE_NAME} on ${TARGET_SHA}..."
"$SYSTEMCTL_BIN" daemon-reload
stop_and_drain_service
"$SYSTEMCTL_BIN" start "$SERVICE_NAME"

atomic_symlink "$FRONTEND_TARGET_DIR" "$FRONTEND_SYMLINK"
touch "$RELEASE_TARGET_DIR" "$FRONTEND_TARGET_DIR"

# ---------------------------------------------------------------------------
# Post-Switch Smoke Checks (Same R2 Coverage as deploy.sh)
# ---------------------------------------------------------------------------
VER_OUT="$("${BACKEND_BIN_DIR}/dokitool-go" version 2>/dev/null || true)"
if [ "$VER_OUT" != "$TARGET_SHA" ]; then
  echo "Error: rollback smoke check failed: dokitool-go version=${VER_OUT}, want ${TARGET_SHA}" >&2
  exit 1
fi

INSTALLED_SHA="$(sha256_file "${BACKEND_BIN_DIR}/dokitool-go")"
if [ "$INSTALLED_SHA" != "$EXPECTED_DOKITOOL_SHA256" ]; then
  echo "Error: rollback smoke check failed: binary SHA-256=${INSTALLED_SHA}, want ${EXPECTED_DOKITOOL_SHA256}" >&2
  exit 1
fi

if ! "$SYSTEMCTL_BIN" is-active --quiet "$SERVICE_NAME"; then
  echo "Error: rollback smoke check failed: ${SERVICE_NAME} is not active" >&2
  exit 1
fi

LOCAL_RESP=""
for _ in $(seq 1 25); do
  if LOCAL_RESP="$("$CURL_BIN" -sS --max-time 3 "${LOCAL_API_URL%/}/api/platforms?schema_version=1.0" 2>/dev/null)" && [ -n "$LOCAL_RESP" ]; then
    break
  fi
  sleep 0.2
done
if [ -z "$LOCAL_RESP" ]; then
  echo "Error: rollback smoke check failed: local API listener at ${LOCAL_API_URL%/}/api/platforms?schema_version=1.0 unreachable" >&2
  exit 1
fi
LOCAL_RESP_ENV="$LOCAL_RESP" "$PYTHON_BIN" - <<'PY'
import json
import os
import sys

payload = json.loads(os.environ["LOCAL_RESP_ENV"])
if payload.get("code") not in ("OK", "SERVICE_NOT_CONFIGURED"):
    sys.exit(1)
PY

PUBLIC_PLATFORMS_JSON=""
for _ in $(seq 1 10); do
  if PUBLIC_PLATFORMS_JSON="$("$CURL_BIN" -fsS --max-time 5 "${PUBLIC_SITE_URL%/}/api/platforms?schema_version=1.0" 2>/dev/null)"; then
    break
  fi
  sleep 0.2
done
if [ -z "$PUBLIC_PLATFORMS_JSON" ]; then
  echo "Error: rollback smoke check failed: ${PUBLIC_SITE_URL%/}/api/platforms?schema_version=1.0 unreachable" >&2
  exit 1
fi

PLATFORMS_JSON_ENV="$PUBLIC_PLATFORMS_JSON" MIN_PLATFORMS_ENV="$MIN_PLATFORMS" EXPECTED_PLATFORMS_ENV="$EXPECTED_PLATFORMS" "$PYTHON_BIN" - <<'PY'
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

HOMEPAGE_HTML="$("$CURL_BIN" -fsS --max-time 5 "${PUBLIC_SITE_URL%/}/")"
ASSET_REFS=$(HOMEPAGE_HTML_ENV="$HOMEPAGE_HTML" "$PYTHON_BIN" - <<'PY'
import os
import re
import sys

html = os.environ["HOMEPAGE_HTML_ENV"]
refs = sorted(set(re.findall(r"(?:^|/|[(\s\x22\x27])(assets/[A-Za-z0-9._/-]+)", html)))
if not refs:
    sys.exit(1)
for r in refs:
    print(r)
PY
)

while IFS= read -r asset_rel; do
  [ -n "$asset_rel" ] || continue
  "$CURL_BIN" -fsS --max-time 5 -o /dev/null "${PUBLIC_SITE_URL%/}/${asset_rel}"
done <<< "$ASSET_REFS"

if [ -n "$PREV_ASSET_SAMPLE" ]; then
  "$CURL_BIN" -fsS --max-time 5 -o /dev/null "${PUBLIC_SITE_URL%/}/assets/${PREV_ASSET_SAMPLE}"
fi

log "Rollback to ${TARGET_SHA} succeeded and passed smoke checks."
