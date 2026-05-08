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

The decoder is grounded in **the manufacturer's own VB6 source** committed
at [`.support/LP-500_DataLogger/`](.support/LP-500_DataLogger/) — namely
`USB_HID_Functions.bas` (the read/write API) and `FrmSetup.frm` (the
button handlers + the labelled IN-report dump). Cross-referenced against
the KD4Z **LP-500/700 HID DIRECT v1.2** Node-RED flow at
[`.support/LP700NodeRed flow.json`](.support/LP700NodeRed%20flow.json) for
the power/SWR scaling. Everything in this section is taken from those two
sources.

### Connection (confirmed)

- **Class:** Vendor-Specific HID. No special drivers — Windows recognises
  the meter as a generic HID device, the Linux kernel binds it via
  `hid-generic` and exposes `/dev/hidraw*`.
- **VID:PID:** `0x04D8:0x0001` (Microchip USB VID + product 1; the
  DataLogger source has `MyVendorID = &H4D8` / `MyProductID = &H1`).
- **Reports:** 64-byte fixed-size, with a Report ID byte the OS prepends
  on Read/Write. The DataLogger reads/writes 65 bytes via Win32 `ReadFile`/
  `WriteFile` where `ReceiveBuffer(0)` / `DataToSend(0)` is the RID. On
  Linux hidraw the same convention applies (writeReport in `owner.go`
  prepends the RID).
- **Polling cadence:** the host actively polls by writing the `'0'` (48,
  0x30) command. Default in this server is ~25 Hz (40 ms), tunable via
  `meter.poll_ms`.

### Wire format (confirmed by the DataLogger source)

Both the OUT (host→meter) and IN (meter→host) reports are 64-byte vendor
reports with **the meaningful data starting at byte 0** of the report
payload. The OS layer's RID byte is at buffer index 0 and the report
payload starts at buffer index 1; on the wire they're **64 bytes**.

```
host-side buffer (65 bytes) ──── OS strips byte[0] (RID) ───►   wire (64 bytes)
       buf[0] = RID = 0x00                                       payload[0]  ← command/status byte
       buf[1] = command/status byte                              payload[1..63] ← data
       buf[2..64] = data
```

DataLogger's `OutputReportData(1) = 48` (and our `EncodeCommand` /
`PollReport` setting `out[0] = '0'`) both produce a wire payload whose
**first byte is the command character**.

### IN-report layout (DataLogger byte map)

Offsets are 0-based against the 64-byte payload that Linux hidraw delivers
(no RID prefix). These are the DataLogger's `InputReportData(N)` indices
minus 1.

| Offset | Field           | Encoding                                                                  |
|-------:|-----------------|---------------------------------------------------------------------------|
|      2 | SWR (hi byte)   | 16-bit BE split with offset 37; `swr = ((hi<<8) \| lo) / 100`             |
|      3 | top mode        | 0=Power/SWR, 1=Waveform, 2=Spectrum, 3=Setup                              |
|      4 | active channel  | 0=Auto, 1..4=CH1..CH4                                                     |
|      5 | channel-auto    | when channel==0, the physical channel auto-mode is using                  |
|      6 | range           | 0..10 = 5W..10KW, 11 = Auto                                               |
|      7 | alarm enable    | 0=off, non-zero=on                                                        |
|      8 | peak/avg/tune   | 0=Average, 1=Peak Hold, 2=Tune                                            |
|      9 | alarm setpoint  | 0..N — alarm threshold index                                              |
|  10–22 | scope/spec/FFT  | wfm CH/Rng/Pst/Style, Test Tone, Trig, Sweep, Knob, FFT CH/Gain/BW/Avg, AutoGain |
|     23 | Peak power Hi   | BE u16; `watts = raw * 0.2`                                               |
|     24 | Peak power Lo   |                                                                           |
|     25 | Avg power Hi    | BE u16; `watts = raw * 0.2`                                               |
|     26 | Avg power Lo    |                                                                           |
|  27–29 | bargraph scaling| BG_avg, BG_pk, BG_SWR — display scaling for the on-LCD bargraphs          |
|     30 | Pwr Mult Hi     | BE u16; `scale = raw / 2` (active full-scale watts of the bargraph)       |
|     31 | Pwr Mult Lo     |                                                                           |
|     32 | Peak/Avg mode   | display variant (separate from offset 8)                                  |
|     33 | Filter          | filter on/off (for spectrum mode subcarrier filter)                       |
|     34 | Freeze          | waveform/spectrum freeze flag                                             |
|     37 | SWR Lo byte     | see offset 2                                                              |

The decoder fills in: `Channel`, `AutoChannel`, `Range`, `TopMode`,
`PowerAvgW`, `PowerPeakW`, `SWR`, `AlarmEnabled`, `PeakMode`. The other
fields (BG_*, scope/spec/FFT) aren't surfaced in the v1 web client because
v1 mirrors only the Power/SWR display.

**Still unknown (not in the DataLogger's IN-report dump):**
callsign, coupler model, firmware revision, alarm power threshold (W),
alarm SWR threshold (numeric, vs the index at offset 9). The simulator
populates these so the wire shape stays stable; the real decoder leaves
them at zero/empty until we find them in a separate "get setup" report.

### OUT-report control bytes (DataLogger button map)

| Verb            | payload byte[0] | Source                                                  |
|-----------------|----------------:|---------------------------------------------------------|
| poll            | `'0'` 0x30 (48) | `ReadAndWriteToDevice` in FrmSetup.frm                  |
| `mode_step`     | `'7'` 0x37 (55) | `btn1_Click` ('Mode button')                            |
| `channel_step`  | `'8'` 0x38 (56) | `btn2_Click` ('Channel button')                         |
| `range_step`    | `'9'` 0x39 (57) | `btn3_Click` ('Range button')                           |
| `alarm_toggle`  | `':'` 0x3A (58) | `btn4_Click` (= F4 AL on the meter LCD)                 |
| `peak_toggle`   | `';'` 0x3B (59) | `btn5_Click` (= F5 Peak/Avg/Tune)                       |
| `setup`         | `'<'` 0x3C (60) | `btn6_Click` (= F6 Setup; toggles in/out)               |
| `freeze`        | `'?'` 0x3F (63) | `cmdFreeze_Click` (waveform/spectrum modes only)        |

Buttons 61 and 62 are present in `FrmSetup.frm` without comments —
likely scope-mode-only touch buttons (Cursor, Trig). Not exposed in v1
because v1 doesn't mirror the scope view.

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
