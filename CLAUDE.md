# LP-700 Server

A WebSocket server that bridges the **Telepost LP-500 / LP-700 Digital Station
Monitor** to multiple networked client applications. One server process owns
the USB HID connection to the meter; many clients subscribe to telemetry and
may issue control actions. When any client invokes an action, the resulting
state change is broadcast to every other client.

This project is the LP-500/700 sibling of [VU3ESV/LP-100A-Server][lp100a].
The architecture, fan-out semantics, configuration layout, deployment scripts,
and WebSocket frame shape are deliberately copied from there. The only thing
that is materially different is the meter link layer — **HID** for the LP-500/
700, **VCP serial** for the LP-100A.

[lp100a]: https://github.com/VU3ESV/LP-100A-Server

## Repository layout

```
LP-700-Server/
├── CLAUDE.md            # this file
├── README.md            # build / install / operate
├── ARCHITECTURE.md      # design proposal for the server + clients
├── main.go              # entry point: wires config -> hid -> hub -> http
├── go.mod / go.sum
├── internal/
│   ├── config/          # TOML loader with defaults + validation
│   ├── lpmeter/         # HID owner, frame decoder, snapshot model, simulator, probe
│   ├── hub/             # WebSocket hub: fan-out, fan-in, heartbeats
│   └── web/static/      # reference web client (embedded via go:embed)
├── deploy/              # build-pi.sh, install.sh, redeploy.sh, systemd unit, udev rule
└── .support/            # reference material (do not modify)
    └── links.txt        # vendor + community references
```

## What the meter exposes (Power/SWR mode)

The LP-500 and LP-700 are functionally the same instrument; they differ only
in display size (5" vs 7"). Every reference in this repo to "LP-700" applies
equally to the LP-500. The meter is a four-channel directional wattmeter +
SWR meter with a colour TFT and three top-level display modes:

1. **Power/SWR** — Average / Peak power (W), SWR, alarm status, with one of
   four coupler channels active (or Auto-channel).
2. **Waveform/'Scope** — modulation envelope at various sweep rates; trapezoid
   linearity displays; AM-mod, Wfm/Pwr, Trap/Pwr split screens. **Not mirrored
   in v1.** The protocol bandwidth needed to ship full waveform frames over
   WebSocket is out of scope — this server reflects Power/SWR-mode telemetry
   only and exposes mode-switch controls so the operator can drive the meter's
   on-LCD scope/spectrum from any client.
3. **Spectrum** — FFT of the modulation. **Not mirrored in v1**, same reason.

The server's job is to mirror the **Power/SWR control surface** across many
clients (the way the vendor's "VM" Windows app mirrors a single LP-500/700 to
a single Windows desktop) and to expose mode-switching so an operator can use
the on-meter scope/spectrum without reaching for the physical front panel.

## LP-500 / LP-700 USB HID protocol

The protocol decoder is grounded in the **KD4Z LP-500/700 HID DIRECT v1.2**
Node-RED reference flow committed at [`.support/LP700NodeRed flow.json`](.support/LP700NodeRed%20flow.json),
which is known to work against real hardware. The flow's `LP Dice and Slice`
function is the definitive parser for the IN report; its `Poll Meter Values`,
`Change Channel`, and `Change Range` functions define three confirmed OUT
commands. Anything in this section that comes from those functions is
labelled **confirmed**. Verbs and offsets the flow doesn't cover are
labelled **hypothesis** and are validated by the bring-up plan in
[ARCHITECTURE.md §11](ARCHITECTURE.md).

### Connection (confirmed)

- **Class:** Vendor-Specific HID. No special drivers — Windows recognises
  the meter as a generic HID device, the Linux kernel binds it via
  `hid-generic` and exposes `/dev/hidraw*`.
- **VID:PID:** `0x04D8:0x0001` (Microchip USB VID + product 1; the Node-RED
  HIDConfig records this as decimal `vid: "1240" pid: "1"`).
- **Reports:** 64-byte fixed-size, **no Report ID**. The mikroElektronika
  USB HID stack used by the PIC32 firmware (confirmed by the
  `mikroBootloader` flow on page 12 of `LP-500-User_Guide.pdf`) sends a
  single 64-byte vendor IN report and accepts a single 64-byte vendor OUT
  report. The first byte of the OUT report is reserved (`0x00`); the
  command byte sits at offset 1.
- **Polling cadence:** the host actively polls by writing the `'0'` (0x30)
  command. The default in this server is ~25 Hz (40 ms), tunable via
  `meter.poll_ms`.

### IN report layout (confirmed)

Offsets are 0-based against the 64-byte buffer the decoder receives.

| Offset | Size | Field                  | Encoding                                        |
|-------:|-----:|------------------------|-------------------------------------------------|
|      2 |    1 | SWR high byte          | split with offset 37; `swr = ((hi<<8)|lo)/100`  |
|      3 |    1 | top-mode               | 0=Power/SWR, 1=Waveform, 2=Spectrum, 3=Setup *(hypothesis)* |
|      4 |    1 | active channel         | 0=Auto, 1..4=CH1..CH4                           |
|      5 |    1 | channel-auto locked-to | physical channel auto-mode is currently using  |
|      6 |    1 | range                  | 0..10 = 5W, 10W, 25W, 50W, 100W, 250W, 500W, 1KW, 2.5KW, 5KW, 10KW; 11 = Auto |
|     23 |    2 | Peak power BE u16      | `watts = raw * 0.2` (== `raw * 2 / 10`)         |
|     25 |    2 | Avg power BE u16       | `watts = raw * 0.2`                             |
|     30 |    2 | Power multiplier BE u16| `scale = raw / 2` (== `raw * 2 / 4`); the active full-scale watts of the bargraph |
|     37 |    1 | SWR low byte           | see offset 2                                    |

Bytes outside the table above are reserved for fields the Node-RED flow
doesn't decode (alarm thresholds, peak/avg/tune, callsign, coupler model,
firmware revision). The decoder leaves those at zero / empty when reading
real frames; the simulator backend populates them so the wire-shape stays
stable for clients.

### OUT-report control bytes

| Verb            | byte[1]    | Confirmed?  | Effect                                |
|-----------------|-----------:|-------------|---------------------------------------|
| `poll`          | `'0'` 0x30 | yes         | Ask for a fresh telemetry frame       |
| `mode_step`     | `'1'` 0x31 | hypothesis  | Cycle top-level Mode                  |
| `alarm_toggle`  | `'7'` 0x37 | hypothesis  | Enable / disable alarms               |
| `peak_toggle`   | `'3'` 0x33 | hypothesis  | Cycle Average / Peak-Hold / Tune      |
| `setup_enter`   | `'4'` 0x34 | hypothesis  | Open Setup screen                     |
| `setup_exit`    | `'5'` 0x35 | hypothesis  | Return to Power/SWR                   |
| `power_mode`    | `'6'` 0x36 | hypothesis  | Net / Delivered / Forward             |
| `channel_step`  | `'8'` 0x38 | yes         | Advance to next channel (1..4 → Auto) |
| `range_step`    | `'9'` 0x39 | yes         | Advance to next range step            |

**Wire format** (every OUT report):

```
buf[0] = 0x00   ; Report-ID byte for karalabe/hid (pre-pended automatically by writeReport)
buf[1] = ASCII command character (above)
buf[2..63] = 0
```

Note that `karalabe/hid` writes a 65-byte buffer where buf[0] is the
host-side Report ID; the 64-byte payload starts at buf[1]. Inside the
payload, the firmware expects byte 0 to be `0x00` and byte 1 to be the
command character — so the value-of-interest lives at *payload byte 1*,
matching the `outbuff[1] = 48` convention in the Node-RED flow.

### Why HID and not VCP

Telepost moved away from FTDI USB-VCP (used by the LP-100A) for the
LP-500/700, presumably to avoid the historical Windows FTDI-driver pain and
because the PIC32 firmware uses the Microchip + mikroElektronika USB HID
class natively. The practical implications for this server:

- No baud rate, no flow control, no `/dev/ttyUSB*`. The meter is a
  `/dev/hidraw*` (Linux) or a `\\\\.\\HID#…` instance ID (Windows).
- The HID layer in this server talks to **`/dev/hidraw*` directly** via
  plain file I/O and reads sysfs (`/sys/class/hidraw/*/device/uevent`)
  for enumeration — **pure Go, no CGO, no `hidapi`/`libusb`/`libudev`
  build dependencies**. This matches the LP-100A-Server philosophy:
  `GOOS=linux GOARCH=arm64 go build` works from any dev box with a Go
  toolchain, no `apt`/`brew install` of cross-gcc required.
- The trade-off is portability: HID support is **Linux-only** in this
  build. macOS (IOHIDDevice) and Windows (HID setupapi) are not wired
  up — the simulator backend works on every platform, but a real meter
  needs a Linux host (Pi or otherwise). See `internal/lpmeter/hid_other.go`
  for the stub returned on non-Linux builds.

## Stack decisions (locked in)

- **Language:** Go. Same rationale as LP-100A — single static binary per
  platform, easy ARM64 deploy.
- **HID library:** none. Direct `/dev/hidraw*` I/O on Linux; stub
  elsewhere. Pure Go ⇒ pure cross-compile.
- **WebSocket:** `gorilla/websocket`, same as LP-100A.
- **Config:** TOML via `BurntSushi/toml`.
- **Logging:** `log/slog` with a runtime-flippable level
  (`/api/log-level`).
- **Deployment:** the Pi (Raspberry Pi OS, 64-bit) is the primary target.
  Windows and macOS builds also ship as the simulator backend (handy for
  developing the web client without hardware).
- **Auth:** none. Bind to LAN; network-level trust only.

## Backends

The server has two interchangeable telemetry sources, picked via
`-backend` or `[meter] backend = "..."` in the config:

| Backend     | Purpose                                                            |
|-------------|--------------------------------------------------------------------|
| `hid`       | Real LP-500/700 over USB HID. The default in production.           |
| `simulator` | Synthesises a believable telemetry stream so the WS hub, web UI, and clients can be developed without hardware. Walks through ranges, toggles peak-mode, raises alarms periodically. |

The `simulator` backend is the default when the binary is run with no config
file and no meter present. The `hid` backend is selected automatically when
`port = "auto"` finds a matching device.

## Diagnostic subcommands

Beyond serving traffic, the binary provides hardware-bring-up subcommands:

```sh
# enumerate every HID on the system, marking those whose product/manufacturer
# string mentions LP-500 / LP-700
lp700-server probe -list

# open the matched LP-500/700 and dump every IN report (raw hex + best-effort
# decode), one line per frame, until ^C. Useful for verifying the byte
# offsets in internal/lpmeter/decode.go.
lp700-server probe -dump

# write each unique frame seen during a 5-second window to a file;
# the test suite (decode_test.go) reads these as fixtures.
lp700-server probe -capture lp500-fixture.bin
```

These never run the WS server — they're hardware-only.

## HTTP surface

The same binary serves both the WebSocket and the embedded web client:

| Path             | Method     | Purpose                                                                |
|------------------|------------|------------------------------------------------------------------------|
| `/`              | GET        | Embedded reference web client (`internal/web/static/index.html`)       |
| `/ws`            | GET (WS)   | WebSocket endpoint — telemetry + control verbs (see ARCHITECTURE.md §4)|
| `/healthz`       | GET        | Liveness probe (returns `ok\n`)                                        |
| `/api/config`    | GET        | UI bootstrap: `{allow_control, backend, callsign}`                     |
| `/api/log-level` | GET / POST | Read or change the running server's slog level                         |

The default log level is **error** so the journal stays quiet on a Pi; pass
`-v` to start at debug, or flip it at runtime via `/api/log-level`.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.
