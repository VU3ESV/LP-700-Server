#!/usr/bin/env bash
# Push a fresh lp700-server build to a Raspberry Pi over SSH and bounce
# the service. Mirrors the LP-100A-Server redeploy.sh flow.
#
# Examples:
#   ./deploy/redeploy.sh pi@raspberrypi.local            # rebuild, scp, restart
#   ./deploy/redeploy.sh pi@host --service               # also push the systemd unit
#   ./deploy/redeploy.sh pi@host --keep-config           # binary only (skip toml/udev)
#   ./deploy/redeploy.sh pi@host --restart-only          # skip build, just bounce
#   ARCH=arm ./deploy/redeploy.sh pi@host                # 32-bit ARMv7 target
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ "$#" -lt 1 ]; then
  echo "usage: $0 user@host [--service] [--keep-config] [--restart-only]" >&2
  exit 2
fi

REMOTE="$1"
shift

PUSH_SERVICE=0
KEEP_CONFIG=0
RESTART_ONLY=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --service)      PUSH_SERVICE=1 ;;
    --keep-config)  KEEP_CONFIG=1 ;;
    --restart-only) RESTART_ONLY=1 ;;
    *)              echo "unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

ARCH="${ARCH:-arm64}"
case "$ARCH" in
  arm64) BIN_NAME="lp700-server-linux-arm64" ;;
  arm)   BIN_NAME="lp700-server-linux-armv7" ;;
  *) echo "unknown ARCH: $ARCH" >&2; exit 2 ;;
esac

BIN_PATH="$PROJECT_DIR/dist/$BIN_NAME"

if [ "$RESTART_ONLY" -eq 0 ]; then
  echo "==> building $BIN_NAME"
  ARCH="$ARCH" "$SCRIPT_DIR/build-pi.sh"
fi

ts() { date +%Y%m%d-%H%M%S; }

if [ "$RESTART_ONLY" -eq 0 ]; then
  echo "==> uploading binary"
  scp "$BIN_PATH" "$REMOTE:/tmp/lp700-server.new"
  ssh "$REMOTE" sudo install -m 0755 /tmp/lp700-server.new /usr/local/bin/lp700-server
fi

if [ "$PUSH_SERVICE" -eq 1 ]; then
  echo "==> uploading systemd unit"
  scp "$SCRIPT_DIR/lp700-server.service" "$REMOTE:/tmp/lp700-server.service"
  ssh "$REMOTE" sudo install -m 0644 /tmp/lp700-server.service /etc/systemd/system/lp700-server.service
fi

if [ "$KEEP_CONFIG" -eq 0 ] && [ "$RESTART_ONLY" -eq 0 ]; then
  echo "==> refreshing config + udev rule (preserving previous values)"
  STAMP="$(ts)"
  scp "$SCRIPT_DIR/config.example.toml" "$REMOTE:/tmp/config.toml.new"
  scp "$SCRIPT_DIR/99-lp700.rules"      "$REMOTE:/tmp/99-lp700.rules.new"

  # If a previous config exists, lift the [meter] vendor_id/product_id
  # values (they're hardware-specific) into the new template so a manual
  # override survives the redeploy.
  ssh "$REMOTE" "bash -se" <<EOF
set -eu
sudo install -d -m 0755 /etc/lp700-server
if [ -f /etc/lp700-server/config.toml ]; then
  sudo cp /etc/lp700-server/config.toml /etc/lp700-server/config.toml.bak.$STAMP
  vid=\$(awk -F= '/^vendor_id/{gsub(/[ \t]/,"",\$2);print \$2}'  /etc/lp700-server/config.toml || true)
  pid=\$(awk -F= '/^product_id/{gsub(/[ \t]/,"",\$2);print \$2}' /etc/lp700-server/config.toml || true)
  if [ -n "\$vid" ] && [ "\$vid" != "0x0000" ]; then
    sudo sed -i "s|^vendor_id  = .*|vendor_id  = \$vid|" /tmp/config.toml.new
  fi
  if [ -n "\$pid" ] && [ "\$pid" != "0x0000" ]; then
    sudo sed -i "s|^product_id = .*|product_id = \$pid|" /tmp/config.toml.new
  fi
fi
sudo install -m 0644 /tmp/config.toml.new /etc/lp700-server/config.toml
if [ -f /etc/udev/rules.d/99-lp700.rules ]; then
  sudo cp /etc/udev/rules.d/99-lp700.rules /etc/udev/rules.d/99-lp700.rules.bak.$STAMP
fi
sudo install -m 0644 /tmp/99-lp700.rules.new /etc/udev/rules.d/99-lp700.rules
sudo udevadm control --reload-rules
sudo udevadm trigger
EOF
fi

echo "==> reloading systemd + restarting service"
ssh "$REMOTE" 'sudo systemctl daemon-reload && sudo systemctl restart lp700-server && systemctl is-active lp700-server'

echo "==> done. Tail with:  ssh $REMOTE journalctl -u lp700-server -f"
