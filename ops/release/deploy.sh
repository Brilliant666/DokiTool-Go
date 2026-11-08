#!/usr/bin/env bash
# Method B Production Release Deployment Script
# Usage: ops/release/deploy.sh <40-char-commit-sha>
#
# Security & Operational Invariants:
# - Accepts ONLY a full 40-character hexadecimal commit SHA (no branch names or 'latest').
# - Reads explicit paths, arguments, and URLs from /etc/dokitool/release.conf (0644, root-owned, no secrets)
#   with zero fallback defaults in the script.
# - Compares active systemd ExecStart arguments against EXEC_ARGS before making any changes.
# - Refuses to overwrite the currently active release SHA.
# - Verifies BOTH release-manifest.json SHA-256 digests AND GitHub build provenance attestations
#   (--signer-workflow and --source-ref refs/heads/main) before modifying disk state.
# - Never reads, prints, or writes secret/token values or environment files.
# - Preserves previous hashed frontend assets (/assets/*) across releases and never unregisters Service Workers.
# - Verifies binary version/SHA-256, systemd active state, local listener, public /api/platforms,
#   public homepage + all referenced assets/*, and preserved previous-release hashed chunk;
#   automatically rolls back 90-release-exec.conf and FRONTEND_SYMLINK on any failure.
# - Only prunes directories containing .method-b-release that are not referenced by any systemd drop-in or FRONTEND_SYMLINK.

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
SIGNER_WORKFLOW="${DOKITOOL_SIGNER_WORKFLOW:-${REPO}/.github/workflows/release-build.yml}"
SOURCE_REF="${DOKITOOL_SOURCE_REF:-refs/heads/main}"

RELEASE_CONF="${DOKITOOL_RELEASE_CONF:-/etc/dokitool/release.conf}"
SERVICE_NAME="${DOKITOOL_SERVICE_NAME:-dokitool-go.service}"
SYSTEMD_DROPIN_DIR="${DOKITOOL_SYSTEMD_DROPIN_DIR:-/etc/systemd/system/${SERVICE_NAME}.d}"
DROPIN_FILE="${SYSTEMD_DROPIN_DIR}/${DOKITOOL_DROPIN_FILENAME:-90-release-exec.conf}"

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

PREV_FRONTEND_TARGET=""
if [ -L "$FRONTEND_SYMLINK" ]; then
  PREV_FRONTEND_TARGET="$(readlink "$FRONTEND_SYMLINK")"
fi

if [ "$ACTIVE_EXEC_PATH" = "${BACKEND_BIN_DIR}/dokitool-go" ] || \
   [[ "$ACTIVE_EXEC_PATH" == "${RELEASE_TARGET_DIR}/"* ]] || \
   [ "$PREV_FRONTEND_TARGET" = "$FRONTEND_TARGET_DIR" ]; then
  echo "Error: target SHA ${TARGET_SHA} is already the currently active release; refusing to overwrite active release directory" >&2
  exit 1
fi

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
log "Verifying GitHub build provenance attestations (--signer-workflow ${SIGNER_WORKFLOW} --source-ref ${SOURCE_REF})..."
for asset in dokitool shanghai-resolver frontend-dist.tar.gz release-manifest.json; do
  "$GH_BIN" attestation verify "${STAGE_DIR}/${asset}" \
    --repo "$REPO" \
    --signer-workflow "$SIGNER_WORKFLOW" \
    --source-ref "$SOURCE_REF" >/dev/null
done
log "All 4 release artifacts passed gh attestation verify."

# ---------------------------------------------------------------------------
# Phase 4: Record Pre-Deploy State for Atomic Rollback
# ---------------------------------------------------------------------------
mkdir -p "$RELEASES_ROOT" "$SYSTEMD_DROPIN_DIR" "$(dirname "$FRONTEND_SYMLINK")"

HAD_PREV_DROPIN=0
PREV_DROPIN_BACKUP="${STAGE_DIR}/prev-dropin.conf"
if [ -f "$DROPIN_FILE" ]; then
  HAD_PREV_DROPIN=1
  cp -p "$DROPIN_FILE" "$PREV_DROPIN_BACKUP"
fi

PREV_ASSET_SAMPLE=""
if [ -n "$PREV_FRONTEND_TARGET" ] && [ -d "${PREV_FRONTEND_TARGET}/assets" ]; then
  PREV_ASSET_SAMPLE="$(
    PREV_ASSETS_ENV="${PREV_FRONTEND_TARGET}/assets" NEW_ASSETS_ENV="${STAGE_DIR}/frontend-extracted/assets" "$PYTHON_BIN" - <<'PY'
import os
from pathlib import Path

prev_root = Path(os.environ["PREV_ASSETS_ENV"])
new_root = Path(os.environ["NEW_ASSETS_ENV"])
prev_files = sorted(p.relative_to(prev_root).as_posix() for p in prev_root.rglob("*") if p.is_file())
new_files = {p.relative_to(new_root).as_posix() for p in new_root.rglob("*") if p.is_file()} if new_root.is_dir() else set()

only_prev = [f for f in prev_files if f not in new_files]
if only_prev:
    print(only_prev[0])
elif prev_files:
    print(prev_files[0])
PY
  )"
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
  log "Rolling back 90-release-exec.conf and FRONTEND_SYMLINK to pre-deploy state..."
  if [ "$HAD_PREV_DROPIN" -eq 1 ]; then
    cp -p "$PREV_DROPIN_BACKUP" "$DROPIN_FILE"
  else
    rm -f "$DROPIN_FILE"
  fi
  if [ -n "$PREV_FRONTEND_TARGET" ]; then
    atomic_symlink "$PREV_FRONTEND_TARGET" "$FRONTEND_SYMLINK"
  fi
  "$SYSTEMCTL_BIN" daemon-reload || true
  stop_and_drain_service || true
  "$SYSTEMCTL_BIN" start "$SERVICE_NAME" || true
  log "Rollback to previous release completed."
}

# ---------------------------------------------------------------------------
# Phase 5: Stage Release Under ${RELEASES_ROOT}/${TARGET_SHA}
# ---------------------------------------------------------------------------
mkdir -p "$BACKEND_BIN_DIR" "${RELEASE_TARGET_DIR}/frontend"
install -m 0755 "${STAGE_DIR}/dokitool" "${BACKEND_BIN_DIR}/dokitool-go"
install -m 0755 "${STAGE_DIR}/shanghai-resolver" "${BACKEND_BIN_DIR}/shanghai-resolver"
install -m 0644 "${STAGE_DIR}/release-manifest.json" "${RELEASE_TARGET_DIR}/release-manifest.json"
printf '%s\n' "$TARGET_SHA" > "${RELEASE_TARGET_DIR}/.method-b-release"
chmod 0644 "${RELEASE_TARGET_DIR}/.method-b-release"

rm -rf "${FRONTEND_TARGET_DIR}.new.$$"
mkdir -p "${FRONTEND_TARGET_DIR}.new.$$"
cp -a "${STAGE_DIR}/frontend-extracted/." "${FRONTEND_TARGET_DIR}.new.$$/"

if [ -n "$PREV_FRONTEND_TARGET" ] && [ -d "${PREV_FRONTEND_TARGET}/assets" ]; then
  merge_missing_files "${PREV_FRONTEND_TARGET}/assets" "${FRONTEND_TARGET_DIR}.new.$$/assets"
fi

rm -rf "$FRONTEND_TARGET_DIR"
mv "${FRONTEND_TARGET_DIR}.new.$$" "$FRONTEND_TARGET_DIR"
touch "$RELEASE_TARGET_DIR" "$FRONTEND_TARGET_DIR"

# ---------------------------------------------------------------------------
# Phase 6: Write ExecStart-Only Systemd Drop-in, Drain/Restart, and Switch Symlink
# ---------------------------------------------------------------------------
cat > "${DROPIN_FILE}.tmp.$$" <<EOF
[Service]
ExecStart=
ExecStart=${BACKEND_BIN_DIR}/dokitool-go ${EXEC_ARGS}
EOF
chmod 0644 "${DROPIN_FILE}.tmp.$$"
mv -f "${DROPIN_FILE}.tmp.$$" "$DROPIN_FILE"

log "Reloading systemd, draining existing requests, and starting ${SERVICE_NAME}..."
if ! "$SYSTEMCTL_BIN" daemon-reload || ! stop_and_drain_service || ! "$SYSTEMCTL_BIN" start "$SERVICE_NAME"; then
  echo "Error: failed to restart ${SERVICE_NAME} on ${TARGET_SHA}" >&2
  rollback_to_previous
  exit 1
fi

atomic_symlink "$FRONTEND_TARGET_DIR" "$FRONTEND_SYMLINK"

# ---------------------------------------------------------------------------
# Phase 7: Post-Switch Smoke Checks (Auto-Rollback on Any Failure)
# ---------------------------------------------------------------------------
run_smoke_checks() {
  local ver_out
  ver_out="$("${BACKEND_BIN_DIR}/dokitool-go" version 2>/dev/null || true)"
  if [ "$ver_out" != "$TARGET_SHA" ]; then
    echo "Smoke check failed: dokitool-go version returned '${ver_out}', want '${TARGET_SHA}'" >&2
    return 1
  fi

  local installed_sha
  installed_sha="$(sha256_file "${BACKEND_BIN_DIR}/dokitool-go")"
  if [ "$installed_sha" != "$EXPECTED_DOKITOOL_SHA256" ]; then
    echo "Smoke check failed: installed dokitool-go SHA-256 (${installed_sha}) != manifest (${EXPECTED_DOKITOOL_SHA256})" >&2
    return 1
  fi

  if ! "$SYSTEMCTL_BIN" is-active --quiet "$SERVICE_NAME"; then
    echo "Smoke check failed: ${SERVICE_NAME} is not active" >&2
    return 1
  fi

  local local_resp=""
  local attempt
  for attempt in $(seq 1 25); do
    if local_resp="$("$CURL_BIN" -sS --max-time 3 "${LOCAL_API_URL%/}/api/platforms?schema_version=1.0" 2>/dev/null)" && [ -n "$local_resp" ]; then
      break
    fi
    sleep 0.2
  done
  if [ -z "$local_resp" ]; then
    echo "Smoke check failed: local API listener at ${LOCAL_API_URL%/}/api/platforms?schema_version=1.0 did not respond" >&2
    return 1
  fi
  if ! LOCAL_RESP_ENV="$local_resp" "$PYTHON_BIN" - <<'PY'; then
import json
import os
import sys

payload = json.loads(os.environ["LOCAL_RESP_ENV"])
if payload.get("code") not in ("OK", "SERVICE_NOT_CONFIGURED"):
    print(f"Smoke check failed: unexpected local listener response code={payload.get('code')}", file=sys.stderr)
    sys.exit(1)
PY
    return 1
  fi

  local public_platforms_json=""
  for attempt in $(seq 1 10); do
    if public_platforms_json="$("$CURL_BIN" -fsS --max-time 5 "${PUBLIC_SITE_URL%/}/api/platforms?schema_version=1.0" 2>/dev/null)"; then
      break
    fi
    sleep 0.2
  done
  if [ -z "$public_platforms_json" ]; then
    echo "Smoke check failed: GET ${PUBLIC_SITE_URL%/}/api/platforms?schema_version=1.0 did not respond with HTTP 200" >&2
    return 1
  fi

  if ! PLATFORMS_JSON_ENV="$public_platforms_json" MIN_PLATFORMS_ENV="$MIN_PLATFORMS" EXPECTED_PLATFORMS_ENV="$EXPECTED_PLATFORMS" "$PYTHON_BIN" - <<'PY'; then
import json
import os
import sys

payload = json.loads(os.environ["PLATFORMS_JSON_ENV"])
if payload.get("code") != "OK":
    print(f"Smoke check failed: public /api/platforms code={payload.get('code')}", file=sys.stderr)
    sys.exit(1)
platforms = payload.get("data", {}).get("platforms", [])
count = len(platforms)
min_p = int(os.environ["MIN_PLATFORMS_ENV"])
exp_p = os.environ["EXPECTED_PLATFORMS_ENV"].strip()
if exp_p:
    if count != int(exp_p):
        print(f"Smoke check failed: public platform count {count} != expected {exp_p}", file=sys.stderr)
        sys.exit(1)
elif count < min_p:
    print(f"Smoke check failed: public platform count {count} < minimum {min_p}", file=sys.stderr)
    sys.exit(1)
PY
    return 1
  fi

  local homepage_html=""
  if ! homepage_html="$("$CURL_BIN" -fsS --max-time 5 "${PUBLIC_SITE_URL%/}/" 2>/dev/null)" || [ -z "$homepage_html" ]; then
    echo "Smoke check failed: GET ${PUBLIC_SITE_URL%/}/ did not return HTTP 200" >&2
    return 1
  fi

  local asset_refs=""
  if ! asset_refs=$(HOMEPAGE_HTML_ENV="$homepage_html" "$PYTHON_BIN" - <<'PY'
import os
import re
import sys

html = os.environ["HOMEPAGE_HTML_ENV"]
refs = sorted(set(re.findall(r"(?:^|/|[(\s\x22\x27])(assets/[A-Za-z0-9._/-]+)", html)))
if not refs:
    print("Smoke check failed: homepage HTML contains no assets/* references", file=sys.stderr)
    sys.exit(1)
for r in refs:
    print(r)
PY
  ); then
    return 1
  fi

  local asset_rel
  while IFS= read -r asset_rel; do
    [ -n "$asset_rel" ] || continue
    if ! "$CURL_BIN" -fsS --max-time 5 -o /dev/null "${PUBLIC_SITE_URL%/}/${asset_rel}" 2>/dev/null; then
      echo "Smoke check failed: homepage asset GET ${PUBLIC_SITE_URL%/}/${asset_rel} did not return HTTP 200" >&2
      return 1
    fi
  done <<< "$asset_refs"

  if [ -n "$PREV_ASSET_SAMPLE" ]; then
    if ! "$CURL_BIN" -fsS --max-time 5 -o /dev/null "${PUBLIC_SITE_URL%/}/assets/${PREV_ASSET_SAMPLE}" 2>/dev/null; then
      echo "Smoke check failed: previous release hashed chunk GET ${PUBLIC_SITE_URL%/}/assets/${PREV_ASSET_SAMPLE} did not return HTTP 200" >&2
      return 1
    fi
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
# Phase 8: Prune Only Unreferenced .method-b-release Directories Beyond Keep Limit
# ---------------------------------------------------------------------------
prune_old_releases() {
  [ -d "$RELEASES_ROOT" ] || return 0
  RELEASES_ROOT_ENV="$RELEASES_ROOT" \
  DROPIN_DIR_ENV="$SYSTEMD_DROPIN_DIR" \
  FRONTEND_SYMLINK_ENV="$FRONTEND_SYMLINK" \
  PREV_FRONTEND_ENV="$PREV_FRONTEND_TARGET" \
  TARGET_SHA_ENV="$TARGET_SHA" \
  KEEP_RELEASES_ENV="$KEEP_RELEASES" \
  "$PYTHON_BIN" - <<'PY'
import os
import shutil
from pathlib import Path

releases_root = Path(os.environ["RELEASES_ROOT_ENV"])
dropin_dir = Path(os.environ["DROPIN_DIR_ENV"])
frontend_symlink = os.environ["FRONTEND_SYMLINK_ENV"]
prev_frontend = os.environ["PREV_FRONTEND_ENV"]
target_sha = os.environ["TARGET_SHA_ENV"]
keep_n = int(os.environ["KEEP_RELEASES_ENV"])

protected_names = {target_sha}

def add_path_protection(raw_path: str) -> None:
    if not raw_path:
        return
    p = Path(raw_path)
    for candidate in (p, Path(os.path.abspath(raw_path))):
        try:
            rel = candidate.relative_to(releases_root)
            if rel.parts:
                protected_names.add(rel.parts[0])
        except ValueError:
            pass

add_path_protection(frontend_symlink)
add_path_protection(prev_frontend)
if os.path.islink(frontend_symlink):
    link_target = os.readlink(frontend_symlink)
    if not os.path.isabs(link_target):
        link_target = os.path.normpath(os.path.join(os.path.dirname(frontend_symlink), link_target))
    add_path_protection(link_target)
if os.path.exists(frontend_symlink):
    add_path_protection(os.path.realpath(frontend_symlink))

dropin_texts = []
if dropin_dir.is_dir():
    for conf in sorted(dropin_dir.glob("*.conf")):
        try:
            dropin_texts.append(conf.read_text(encoding="utf-8", errors="ignore"))
        except OSError:
            pass
combined_dropins = "\n".join(dropin_texts)

method_b_dirs = []
for entry in releases_root.iterdir():
    if not entry.is_dir() or entry.is_symlink():
        continue
    if entry.name in combined_dropins:
        protected_names.add(entry.name)
    marker = entry / ".method-b-release"
    if not marker.is_file():
        continue
    method_b_dirs.append(entry)

method_b_dirs.sort(key=lambda p: p.stat().st_mtime, reverse=True)

for idx, d in enumerate(method_b_dirs):
    if idx < keep_n or d.name in protected_names:
        continue
    shutil.rmtree(d, ignore_errors=True)
PY
}

prune_old_releases
log "Deployment of ${TARGET_SHA} succeeded."
