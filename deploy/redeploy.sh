#!/usr/bin/env bash
# Redeploy lp700-server to a Raspberry Pi (or any systemd Linux box where
# install.sh has already run once). Run this from your dev machine.
#
# Default scope (run on every redeploy):
#   - /usr/local/bin/lp700-server                  application binary
#   - /etc/lp700-server/config.toml                server config (template)
#   - /etc/udev/rules.d/99-lp700.rules             device rule (template)
#
# Preserved from the existing config (the user's customization survives):
#   - meter.vendor_id and meter.product_id         (hardware-specific IDs)
#
# Backups:
#   The previous /etc/lp700-server/config.toml and /etc/udev/rules.d/99-lp700.rules
#   are saved next to themselves with a .bak.YYYYMMDD-HHMMSS suffix only when the
#   replacement content actually differs (no churn for unchanged files), and only
#   the 3 most recent backups per file are kept.
#
# Optional flags:
#   --service          also reinstall /etc/systemd/system/lp700-server.service
#   --keep-config      skip config.toml + udev rule replacement (binary only)
#   --restart-only     skip the build and the file copies; just bounce the service
#   --no-healthcheck   skip the curl /healthz step at the end
#
# Auth:
#   Recommended  : SSH key auth + passwordless sudo (one quiet redeploy).
#   Also fine    : SSH password + interactive sudo password.
#   Set up keys: ssh-copy-id pi@host
#   Set up passwordless sudo on the Pi:
#     echo "$USER ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/$USER-nopasswd
#
# Prerequisites on the Pi:
#   - install.sh has already run once (creates user, service, paths)
#
# Usage:
#   ./deploy/redeploy.sh pi@raspberrypi.local
#   ARCH=arm ./deploy/redeploy.sh pi@host                 # 32-bit ARMv7
#   ./deploy/redeploy.sh pi@host --service                # also push systemd unit
#   ./deploy/redeploy.sh pi@host --keep-config            # binary only
#   ./deploy/redeploy.sh pi@host --restart-only           # bounce service only
set -euo pipefail
cd "$(dirname "$0")/.."

TARGET="${1:-}"
if [ -z "$TARGET" ]; then
  echo "usage: $0 user@host [--service] [--keep-config] [--restart-only] [--no-healthcheck]" >&2
  exit 1
fi
shift

UPDATE_SERVICE=0
KEEP_CONFIG=0
RESTART_ONLY=0
HEALTHCHECK=1
for arg in "$@"; do
  case "$arg" in
    --service)         UPDATE_SERVICE=1 ;;
    --keep-config)     KEEP_CONFIG=1 ;;
    --restart-only)    RESTART_ONLY=1 ;;
    --no-healthcheck)  HEALTHCHECK=0 ;;
    *) echo "unknown flag: $arg" >&2; exit 1 ;;
  esac
done

ARCH="${ARCH:-arm64}"
case "$ARCH" in
  arm64) BIN_NAME="lp700-server-linux-arm64" ;;
  arm)   BIN_NAME="lp700-server-linux-armv7" ;;
  *) echo "ARCH must be arm64 or arm (got: $ARCH)" >&2; exit 1 ;;
esac
BIN="dist/$BIN_NAME"

# --restart-only implies --keep-config (nothing new to install).
if [ "$RESTART_ONLY" -eq 1 ]; then KEEP_CONFIG=1; fi

if ! command -v ssh >/dev/null 2>&1; then echo "ssh not found in PATH" >&2; exit 1; fi
if ! command -v scp >/dev/null 2>&1; then echo "scp not found in PATH" >&2; exit 1; fi
if [ "$RESTART_ONLY" -eq 0 ] && ! command -v go >/dev/null 2>&1; then
  echo "go not found in PATH (needed to cross-compile; install Go or pass --restart-only)" >&2
  exit 1
fi

step() { printf '\n\033[1;36m>>>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!! %s\033[0m\n' "$*"; }

# --- SSH connection multiplexing -------------------------------------------
# One auth, many operations: scp + ssh share the same control socket so
# the user types their SSH password (if any) once, and sudo password (if
# any) once.
SSH_TMPDIR=$(mktemp -d -t lp700-ssh.XXXXXX)
SSH_CTRL="$SSH_TMPDIR/cm.sock"
SSH_OPTS=(-o "ControlMaster=auto" -o "ControlPath=$SSH_CTRL" -o "ControlPersist=120s")
LOCAL_SCRIPT=""

cleanup() {
  if [ -S "$SSH_CTRL" ]; then
    ssh "${SSH_OPTS[@]}" -O exit "$TARGET" 2>/dev/null || true
  fi
  [ -n "$LOCAL_SCRIPT" ] && rm -f "$LOCAL_SCRIPT"
  rm -rf "$SSH_TMPDIR"
}
trap cleanup EXIT

# --- 1. Build ---------------------------------------------------------------
if [ "$RESTART_ONLY" -eq 0 ]; then
  step "Cross-compiling for $ARCH"
  ARCH="$ARCH" ./deploy/build-pi.sh
  [ -x "$BIN" ] || { echo "build did not produce $BIN" >&2; exit 1; }
fi

# --- 2. Compose the remote install script ----------------------------------
LOCAL_SCRIPT=$(mktemp -t lp700-redeploy.XXXXXX)
build_remote() {
  cat <<'HEAD'
#!/usr/bin/env bash
set -euo pipefail
echo "host: $(hostname) — kernel: $(uname -r)"
TS="$(date +%Y%m%d-%H%M%S)"

# backup_if_changed PATH NEW_CONTENT_PATH — copy PATH to PATH.bak.$TS only
# when the file differs from NEW_CONTENT_PATH (no churn for unchanged files).
# Always prunes any pre-existing .bak.* backups for PATH down to the 3 most
# recent, so historical accumulation gets tidied on every run.
backup_if_changed() {
  local target="$1" new="$2"
  if [ -f "$target" ] && ! sudo cmp -s "$target" "$new"; then
    sudo cp -p "$target" "$target.bak.$TS"
    echo "  backed up: $target.bak.$TS"
  fi
  # Prune: keep newest 3, delete the rest. Runs every time so old chains
  # accumulated before this prune logic existed get cleaned up too.
  sudo bash -c "ls -1t '$target'.bak.* 2>/dev/null | tail -n +4 | xargs -r rm -f"
}

# ---- bootstrap: ensure user, group membership, service unit ----
# Idempotent: only acts when something is missing, so this is safe on
# both first-run and steady-state redeploys.
if ! id -u lp700 >/dev/null 2>&1; then
  echo "  creating system user lp700"
  sudo useradd --system --no-create-home --shell /usr/sbin/nologin lp700
fi
sudo usermod -aG plugdev lp700

FIRST_RUN=0
if [ ! -f /etc/systemd/system/lp700-server.service ]; then
  FIRST_RUN=1
fi

sudo systemctl stop lp700-server 2>/dev/null || true
HEAD

  if [ "$RESTART_ONLY" -eq 0 ]; then
    cat <<HEAD
sudo install -m 0755 -o root -g root "/tmp/$(basename "$BIN")" /usr/local/bin/lp700-server
rm -f "/tmp/$(basename "$BIN")"
HEAD
  fi

  # Force-reinstall flag must be set BEFORE the conditional check below.
  if [ "$UPDATE_SERVICE" -eq 1 ]; then
    cat <<'HEAD'
UPDATE_SERVICE_FORCE=1
HEAD
  fi

  # Install the service unit when it's missing (first-run bootstrap)
  # or when --service was passed.
  cat <<'HEAD'
if [ "$FIRST_RUN" -eq 1 ] || [ "${UPDATE_SERVICE_FORCE:-0}" -eq 1 ]; then
  if [ -f /tmp/lp700-server.service ]; then
    sudo install -m 0644 -o root -g root /tmp/lp700-server.service /etc/systemd/system/lp700-server.service
    rm -f /tmp/lp700-server.service
  else
    echo "lp700-server.service template missing on remote /tmp; first-run bootstrap aborted" >&2
    exit 1
  fi
  sudo systemctl daemon-reload
  sudo systemctl enable lp700-server
fi
HEAD

  if [ "$KEEP_CONFIG" -eq 0 ]; then
    cat <<'HEAD'

# ---- udev rule: backup-if-changed + replace ----
backup_if_changed /etc/udev/rules.d/99-lp700.rules /tmp/99-lp700.rules
sudo install -m 0644 -o root -g root /tmp/99-lp700.rules /etc/udev/rules.d/99-lp700.rules
sudo udevadm control --reload-rules
sudo udevadm trigger
rm -f /tmp/99-lp700.rules

# ---- config.toml: backup-if-changed + replace, preserving meter.vendor_id / product_id ----
sudo install -d -m 0755 /etc/lp700-server
USER_VID_LINE=""
USER_PID_LINE=""
if [ -f /etc/lp700-server/config.toml ]; then
  USER_VID_LINE="$(awk '
    /^[[:space:]]*\[meter\][[:space:]]*$/ { in_meter=1; next }
    /^[[:space:]]*\[/ { in_meter=0 }
    in_meter && /^[[:space:]]*vendor_id[[:space:]]*=/ { print; exit }
  ' /etc/lp700-server/config.toml)"
  USER_PID_LINE="$(awk '
    /^[[:space:]]*\[meter\][[:space:]]*$/ { in_meter=1; next }
    /^[[:space:]]*\[/ { in_meter=0 }
    in_meter && /^[[:space:]]*product_id[[:space:]]*=/ { print; exit }
  ' /etc/lp700-server/config.toml)"
  [ -n "$USER_VID_LINE" ] && echo "  preserving meter.vendor_id: $USER_VID_LINE"
  [ -n "$USER_PID_LINE" ] && echo "  preserving meter.product_id: $USER_PID_LINE"
fi

TMP_NEW="$(mktemp)"
awk -v vid="$USER_VID_LINE" -v pid="$USER_PID_LINE" '
  /^[[:space:]]*\[meter\][[:space:]]*$/ { in_meter=1; print; next }
  /^[[:space:]]*\[/ { in_meter=0; print; next }
  in_meter && /^[[:space:]]*vendor_id[[:space:]]*=/ {
    if (vid != "") { print vid; next }
  }
  in_meter && /^[[:space:]]*product_id[[:space:]]*=/ {
    if (pid != "") { print pid; next }
  }
  { print }
' /tmp/config.example.toml > "$TMP_NEW"
backup_if_changed /etc/lp700-server/config.toml "$TMP_NEW"
sudo install -m 0644 -o root -g root "$TMP_NEW" /etc/lp700-server/config.toml
rm -f "$TMP_NEW" /tmp/config.example.toml
HEAD
  fi

  cat <<'TAIL'
sudo systemctl start lp700-server
sleep 1
if ! sudo systemctl is-active --quiet lp700-server; then
  echo "service failed to start"
  sudo journalctl -u lp700-server -n 30 --no-pager
  exit 1
fi
echo "service: $(systemctl is-active lp700-server) ($(systemctl show lp700-server --property=ActiveEnterTimestamp --value))"
sudo journalctl -u lp700-server -n 8 --no-pager
TAIL
}
build_remote > "$LOCAL_SCRIPT"

# --- 3. Open the SSH control connection ------------------------------------
step "Opening SSH session to $TARGET"
if ! ssh "${SSH_OPTS[@]}" -o "ConnectTimeout=10" "$TARGET" true; then
  echo "could not establish SSH session to $TARGET" >&2
  exit 1
fi

# --- 4. Copy files to /tmp/ on the Pi --------------------------------------
SCP_FILES=()
# Binary + service unit travel together: when not --restart-only, we
# always upload the service unit so the remote can self-bootstrap on
# first run (the in-script FIRST_RUN check decides whether to install
# it). --service forces a reinstall on subsequent runs.
[ "$RESTART_ONLY" -eq 0 ] && SCP_FILES+=("$BIN" deploy/lp700-server.service)
[ "$KEEP_CONFIG"  -eq 0 ] && SCP_FILES+=(deploy/99-lp700.rules deploy/config.example.toml)

step "Copying ${#SCP_FILES[@]} support file(s) + install script to $TARGET:/tmp/"
if [ "${#SCP_FILES[@]}" -gt 0 ]; then
  scp -q "${SSH_OPTS[@]}" "${SCP_FILES[@]}" "$TARGET:/tmp/"
fi
REMOTE_SCRIPT="/tmp/lp700-redeploy.sh"
scp -q "${SSH_OPTS[@]}" "$LOCAL_SCRIPT" "$TARGET:$REMOTE_SCRIPT"

# --- 5. Run install script with a TTY so sudo can prompt --------------------
step "Updating service on $TARGET"
ssh -t "${SSH_OPTS[@]}" "$TARGET" "bash $REMOTE_SCRIPT; rm -f $REMOTE_SCRIPT"

# --- 6. Health check from the dev machine ----------------------------------
if [ "$HEALTHCHECK" -eq 1 ]; then
  HOST="${TARGET#*@}"
  step "Health check: http://$HOST:8089/healthz"
  if curl -sf --max-time 5 "http://$HOST:8089/healthz" >/dev/null; then
    printf '    \033[1;32mOK\033[0m   http://%s:8089/\n' "$HOST"
  else
    warn "/healthz not responding from this host (firewall? bound to a different interface?)"
  fi
fi

step "Done."
