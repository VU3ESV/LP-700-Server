# LP-700 Server

WebSocket bridge for the **Telepost LP-500 / LP-700 Digital Station Monitor**.
One server owns the meter's USB HID handle; many clients (browsers, logging
PCs, phones) get live telemetry and can issue control actions, with every
client kept in sync.

The companion of [VU3ESV/LP-100A-Server](https://github.com/VU3ESV/LP-100A-Server),
covering the LP-500/LP-700 instead of the LP-100A. Same fan-out semantics,
same WebSocket frame shape, same Pi-first deployment story.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the design and [CLAUDE.md](CLAUDE.md)
for the LP-500/700 USB HID protocol reference and decoder hypothesis.

> ℹ️ **Status.** The server, the WebSocket hub, the embedded web client, and
> the simulator backend are functional today. The HID decoder mirrors the
> KD4Z **LP-500/700 HID DIRECT v1.2** Node-RED flow — see
> [CLAUDE.md](CLAUDE.md#lp-500--lp-700-usb-hid-protocol). The IN-report layout,
> VID:PID (0x04D8:0x0001), and three OUT commands (poll, channel-step,
> range-step) are confirmed against real hardware via that flow. Five other
> control verbs are reasonable hypotheses awaiting validation — see
> [ARCHITECTURE.md §11](ARCHITECTURE.md). Run with `-backend simulator` to
> develop without hardware; run `lp700-server probe -dump` against a real
> device to validate the byte offsets.

## Quick start (development, on a Mac)

```sh
go mod tidy
go test ./...

# Run with the simulator backend so you don't need a meter.
go run . -backend simulator -config deploy/config.example.toml
# open http://localhost:8089/
```

The reference web client is embedded in the binary — no separate frontend
build step.

## Building for Raspberry Pi

From any dev machine with Go installed (Mac, Linux, or WSL):

```sh
# Pi 3/4/5 with 64-bit Raspberry Pi OS (recommended):
./deploy/build-pi.sh
# -> dist/lp700-server-linux-arm64

# Pi Zero/1, or 32-bit OS:
ARCH=arm ./deploy/build-pi.sh
# -> dist/lp700-server-linux-armv7
```

The HID layer is pure Go (`/dev/hidraw` + sysfs), so no CGO toolchain is
needed — `GOOS=linux GOARCH=arm64 go build` works with whatever Go you
already have.

## CI / releases

Two GitHub Actions workflows ship with the repo:

- [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push
  and pull request to `main`: `go vet`, `go test -race`, `go build`. The
  Linux job installs `libudev-dev` so the CGO build succeeds.
- [`.github/workflows/release.yml`](.github/workflows/release.yml) is
  triggered automatically when you push a tag matching `v*.*.*` (e.g.
  `git tag v1.0.0 && git push origin v1.0.0`). The workflow:
  1. Cross-compiles binaries for `linux/{amd64,arm64,armv7}`,
     `darwin/{amd64,arm64}`, and `windows/amd64`,
  2. Publishes a GitHub Release with the binaries + a `SHA256SUMS` file
     attached, with changelog auto-generated from commits since the
     previous release.

If you want to ship a release without a tag from the command line, you can
also dispatch the workflow manually from the **Actions** tab — same effect.

## Deploying on the Raspberry Pi

1. Copy the project to the Pi (or just the binary plus `deploy/`):
   ```sh
   scp -r dist/lp700-server-linux-arm64 deploy pi@raspberrypi.local:~/lp700/
   ```
2. SSH in and install:
   ```sh
   ssh pi@raspberrypi.local
   cd ~/lp700
   sudo ./deploy/install.sh ./lp700-server-linux-arm64
   ```
3. Confirm:
   ```sh
   systemctl status lp700-server
   journalctl -u lp700-server -f
   curl http://raspberrypi.local:8089/healthz
   ```

The installer:
- creates a system user `lp700` and adds it to `plugdev` (so the udev rule
  can hand the device to it),
- installs the binary to `/usr/local/bin/lp700-server`,
- writes `/etc/lp700-server/config.toml` (preserving any existing config),
- installs the systemd unit and starts it,
- installs a udev rule that grants the `lp700` user read/write access to
  the LP-500/700 hidraw node.

### Verifying the udev rule

The default rule matches by USB Product string containing `LP-500` or
`LP-700` (which is what the meter exposes — see CLAUDE.md). If your meter
uses different USB IDs, find them and edit the rule:

```sh
lsusb                                              # find the meter's bus/device
udevadm info -a -n /dev/hidraw0 | head -40         # read its idVendor / idProduct / product
sudo nano /etc/udev/rules.d/99-lp700.rules
sudo udevadm control --reload-rules && sudo udevadm trigger
```

If the server reports `no LP-500/700 found`, run `sudo lp700-server probe -list`
to enumerate every HID and find the one that's actually plugged in.

## Redeploying after a code change

After the first install, push subsequent changes from your dev machine with
[`deploy/redeploy.sh`](deploy/redeploy.sh):

```sh
./deploy/redeploy.sh pi@raspberrypi.local           # rebuild, scp, restart
./deploy/redeploy.sh pi@host --service              # also push systemd unit
./deploy/redeploy.sh pi@host --keep-config          # binary only (skip toml/udev)
./deploy/redeploy.sh pi@host --restart-only         # skip build; just bounce the service
ARCH=arm ./deploy/redeploy.sh pi@host               # 32-bit ARMv7 target
```

## Configuration

`/etc/lp700-server/config.toml`. Defaults are sensible; the fields you might
change:

| Field                       | Default          | Notes                                            |
|-----------------------------|------------------|--------------------------------------------------|
| `meter.backend`             | `auto`           | `auto`, `hid`, `simulator`                       |
| `meter.vendor_id`           | `0`              | 0 = match by Product string                      |
| `meter.product_id`          | `0`              | 0 = match by Product string                      |
| `meter.poll_ms`             | `40`             | 25 Hz; the meter pushes IN reports faster than we read them |
| `server.listen`             | `0.0.0.0:8089`   | `:8089` to leave 8088 for `LP-100A-Server`       |
| `server.allow_control`      | `true`           | Set `false` for a read-only port                 |
| `server.max_clients`        | `32`             |                                                  |

After editing: `sudo systemctl restart lp700-server`.

### Logging

Defaults to **error**-level logging so the systemd journal stays quiet. Two
ways to turn it up:

- **At start time:** pass `-v` (sets debug). The deployed unit edits this
  via `systemctl edit lp700-server` →
  `ExecStart=/usr/local/bin/lp700-server -v -config /etc/lp700-server/config.toml`.
- **At runtime, no restart:** open the web client, click **SETUP**, and pick
  a level (ERROR / WARN / INFO / DEBUG). Or hit the API directly:
  ```sh
  curl -s http://host:8089/api/log-level
  curl -s -XPOST http://host:8089/api/log-level -d '{"level":"debug"}'
  ```

## Diagnostic subcommands

The binary doubles as a hardware-bring-up tool:

```sh
# Enumerate every HID, marking those whose product string mentions LP-500/700.
sudo lp700-server probe -list

# Print every IN report (raw hex + best-effort decode), one line per frame.
sudo lp700-server probe -dump

# Capture a window of frames to a file the test suite reads as a fixture.
sudo lp700-server probe -capture lp500-fixture.bin -duration 10s
```

These never run the WS server — they're hardware-only. See
[ARCHITECTURE.md §11](ARCHITECTURE.md) for the full bring-up plan.

## Security

**There is no authentication.** Anyone with network access to the listen
address can read telemetry and issue control commands. This is intentional
for LAN-only deployment. For remote access use a VPN (Tailscale, WireGuard)
and keep the listen address bound to the VPN interface or `127.0.0.1`.

## HTTP / WebSocket surface

| Path             | Purpose                                                                   |
|------------------|---------------------------------------------------------------------------|
| `/`              | Embedded reference web client                                             |
| `/ws`            | WebSocket — telemetry stream + control verbs (see [ARCHITECTURE.md §4](ARCHITECTURE.md)) |
| `/healthz`       | `ok\n` — for systemd / monitoring probes                                  |
| `/api/config`    | `{allow_control, backend, callsign, title}` — UI bootstrap                |
| `/api/log-level` | GET / POST the running slog level (`error`/`warn`/`info`/`debug`)         |

Smoke test from the command line:

```sh
# requires websocat: brew install websocat
websocat ws://raspberrypi.local:8089/ws
# {"type":"telemetry","seq":1,"ts":"...","data":{"channel":1,"power_avg_w":0,"swr":1.00,...}}
echo '{"type":"command","id":"1","action":"peak_toggle","value":1}' | websocat ws://raspberrypi.local:8089/ws
```

## Layout

```
.
├── main.go                       # entry point: wires config -> hid -> hub -> http
├── internal/
│   ├── config/                   # TOML loader with defaults + validation
│   ├── lpmeter/                  # HID owner, frame decoder, snapshot, simulator, probe
│   ├── hub/                      # WebSocket hub: fan-out, fan-in, heartbeats
│   └── web/static/index.html     # reference web client (embedded via go:embed)
├── deploy/
│   ├── config.example.toml
│   ├── lp700-server.service      # systemd unit
│   ├── 99-lp700.rules            # udev rule for the LP-500/700 HID
│   ├── build-pi.sh               # cross-compile from dev box
│   ├── install.sh                # first-time install on the Pi
│   └── redeploy.sh               # subsequent updates (build + scp + restart)
└── .support/                     # vendor + community references (read-only)
```

## Operations cheatsheet

```sh
# Tail logs
journalctl -u lp700-server -f

# Restart after config change
sudo systemctl restart lp700-server

# Stop / disable
sudo systemctl disable --now lp700-server

# Health check
curl -sf http://localhost:8089/healthz && echo OK

# Run in the foreground for debugging (as the lp700 user)
sudo -u lp700 /usr/local/bin/lp700-server -config /etc/lp700-server/config.toml -v

# Bump log verbosity on a running instance without a restart
curl -s -XPOST http://localhost:8089/api/log-level -d '{"level":"debug"}'
```

## Web client

The embedded page mirrors the LP-500/700 **Power/SWR** screen — Avg/Peak
power, SWR, the active channel, range, alarm thresholds, peak-mode, and the
top-level mode pill. Opening multiple tabs / devices is the point of this
server: any control button pressed on one client is reflected on every
other client within one poll cycle.

The Waveform/'Scope and Spectrum modes are *not* mirrored to the web UI in
v1 (see [ARCHITECTURE.md §10](ARCHITECTURE.md)). The mode-switch button on
the web page still cycles the meter's on-LCD top-level mode, so an operator
can switch the front-panel display from a remote tab.

## Acknowledgements

Telepost Inc. designed and manufactures the LP-500 and LP-700. This project
is unaffiliated; product names and trademarks belong to TelePost. The
LP-500/700 user guide (`http://www.telepostinc.com/Files/LP-500/LP-500-User_Guide.pdf`)
is the source of every measurable parameter in `internal/lpmeter/snapshot.go`.
