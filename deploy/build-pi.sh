#!/usr/bin/env bash
# Cross-compile the lp700-server binary for a Raspberry Pi.
#
# Pi 3 / 4 / 5 with 64-bit Raspberry Pi OS  : ARCH=arm64 (default, recommended)
# Pi Zero / 1 / older 32-bit OS             : ARCH=arm
#
# The HID library uses CGO, so cross-compiling needs an ARM C toolchain:
#   - macOS (Homebrew):
#       brew install messense/macos-cross-toolchains/aarch64-unknown-linux-gnu \
#                    messense/macos-cross-toolchains/armv7-unknown-linux-gnueabihf
#   - Debian/Ubuntu:
#       sudo apt-get install gcc-aarch64-linux-gnu gcc-arm-linux-gnueabihf libudev-dev
#
# If you'd rather skip the cross-toolchain dance, build natively on the Pi:
#       sudo apt-get install -y golang libudev-dev libusb-1.0-0-dev pkg-config build-essential
#       go build -trimpath -ldflags="-s -w" -o lp700-server .
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist

# Resolve module dependencies on first build (idempotent thereafter).
go mod tidy

ARCH="${ARCH:-arm64}"
LDFLAGS="-s -w"
export CGO_ENABLED=1

case "$ARCH" in
  arm64)
    OUT="dist/lp700-server-linux-arm64"
    : "${CC:=aarch64-unknown-linux-gnu-gcc}"
    if ! command -v "$CC" >/dev/null 2>&1; then
      CC="aarch64-linux-gnu-gcc"
    fi
    if ! command -v "$CC" >/dev/null 2>&1; then
      echo "no aarch64 cross-gcc found (looked for aarch64-unknown-linux-gnu-gcc and aarch64-linux-gnu-gcc)" >&2
      echo "install one (see comment at top of this script) or build natively on the Pi." >&2
      exit 1
    fi
    GOOS=linux GOARCH=arm64 CC="$CC" \
      go build -trimpath -ldflags="$LDFLAGS" -o "$OUT" .
    ;;
  arm)
    OUT="dist/lp700-server-linux-armv7"
    : "${CC:=armv7-unknown-linux-gnueabihf-gcc}"
    if ! command -v "$CC" >/dev/null 2>&1; then
      CC="arm-linux-gnueabihf-gcc"
    fi
    if ! command -v "$CC" >/dev/null 2>&1; then
      echo "no armv7 cross-gcc found (looked for armv7-unknown-linux-gnueabihf-gcc and arm-linux-gnueabihf-gcc)" >&2
      exit 1
    fi
    GOOS=linux GOARCH=arm GOARM=7 CC="$CC" \
      go build -trimpath -ldflags="$LDFLAGS" -o "$OUT" .
    ;;
  *)
    echo "Unknown ARCH: $ARCH (expected arm64 or arm)" >&2
    exit 1
    ;;
esac

echo "Built: $OUT"
ls -lh "$OUT"
file "$OUT" 2>/dev/null || true
