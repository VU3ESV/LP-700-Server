#!/usr/bin/env bash
# Install lp700-server on a Raspberry Pi (or any systemd Linux box).
# Run from a directory that contains:
#   - the binary (default: ./lp700-server)
#   - the deploy/ folder (lp700-server.service, 99-lp700.rules, config.example.toml)
#
# Usage: sudo ./deploy/install.sh [path-to-binary]
set -euo pipefail

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="${1:-$PROJECT_DIR/lp700-server}"

if [ ! -x "$BIN" ]; then
  for cand in \
      "$PROJECT_DIR/dist/lp700-server-linux-arm64" \
      "$PROJECT_DIR/dist/lp700-server-linux-armv7" \
      "$PROJECT_DIR/dist/lp700-server"; do
    if [ -x "$cand" ]; then
      BIN="$cand"
      break
    fi
  done
fi

if [ ! -x "$BIN" ]; then
  echo "Binary not found or not executable: $BIN" >&2
  echo "Run deploy/build-pi.sh first, or pass the binary path explicitly." >&2
  exit 1
fi

echo "Using binary: $BIN"

# Runtime libraries needed by the HID layer.
if command -v apt-get >/dev/null 2>&1; then
  apt-get install -y libudev1 || true
fi

if ! id -u lp700 >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin lp700
fi
# plugdev owns /dev/hidraw* per the udev rule below.
usermod -aG plugdev lp700

install -m 0755 "$BIN" /usr/local/bin/lp700-server

install -d -m 0755 /etc/lp700-server
if [ ! -f /etc/lp700-server/config.toml ]; then
  install -m 0644 "$SCRIPT_DIR/config.example.toml" /etc/lp700-server/config.toml
  echo "Wrote default config: /etc/lp700-server/config.toml"
else
  echo "Existing config preserved: /etc/lp700-server/config.toml"
fi

install -m 0644 "$SCRIPT_DIR/lp700-server.service" /etc/systemd/system/lp700-server.service
install -m 0644 "$SCRIPT_DIR/99-lp700.rules"      /etc/udev/rules.d/99-lp700.rules

udevadm control --reload-rules
udevadm trigger
systemctl daemon-reload
systemctl enable --now lp700-server.service

echo
echo "Installed. Inspect with:"
echo "  systemctl status lp700-server"
echo "  journalctl -u lp700-server -f"
echo
echo "Web UI: http://$(hostname -I | awk '{print $1}'):8089/"
