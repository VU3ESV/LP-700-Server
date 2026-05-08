# Architecture: LP-700 / LP-500 WebSocket Server

**Status:** v1 in development · **Owner:** [VU3ESV/LP-700-Server](https://github.com/VU3ESV/LP-700-Server)

## 1. Problem

The Telepost LP-500 and LP-700 Digital Station Monitors are four-channel RF
power meters with a colour TFT display, on-board scope and spectrum modes,
and a USB HID port. The vendor's "VM" (Virtual Meter) Windows app mirrors a
single LP-500/700 to a single Windows desktop — fully bidirectional, but
single-tenant: only one process can own the HID handle at a time, and the VM
runs only on Windows.

Operators want the meter readable from a phone, a logging PC, and a station
dashboard *simultaneously*, with control actions (channel select, range,
alarm enable, peak/avg/tune, mode change) reflected on every screen the
moment any one of them changes. None of the available references do this:

- **Telepost VM** — single-tenant, Windows only, no network surface.
- **node-red-contrib-usbhid** — Node-RED nodes that talk raw HID. KD4Z
  published a working LP-500/700 flow ([`.support/LP700NodeRed flow.json`](.support/LP700NodeRed%20flow.json),
  v1.2 with VU3ESV/LB9KJ updates) that is the protocol ground-truth this
  server inherits. The flow is single-tenant though — each browser tab
  opens its own copy and competes for the HID handle, so it doesn't
  scale to a multi-client station.

A single Go server process that owns the USB HID connection and fans the
state out to many WebSocket clients fills the gap, the same way
`LP-100A-Server` fills the equivalent gap for the LP-100A's serial port.

## 2. Goals & non-goals

**Goals**

1. One server process owns the LP-500/700 HID handle and is the *only* thing
   that talks to it.
2. Many clients connect concurrently over WebSocket and receive a live
   stream of telemetry (channel, average power, peak power, SWR, peak-hold,
   range, peak-mode, alarms, callsign, coupler, firmware revision).
3. Any client may issue `mode_step` / `channel_step` / `range_step` /
   `alarm_toggle` / `peak_toggle` / `setup_enter` / `setup_exit` /
   `power_mode` actions; the resulting state change is broadcast to every
   connected client within one poll cycle.
4. The Pi (64-bit Raspberry Pi OS) is the primary target. The same Go
   sources cross-compile cleanly for Windows/macOS dev boxes too.
5. Survives the meter being unplugged, the cable being yanked, or a client
   disappearing — auto-reconnect on both edges.

**Non-goals (v1)**

- Mirroring the on-meter **Waveform/'Scope** and **Spectrum** views. Both
  are fundamentally streaming-frame interfaces (the meter pushes
  multi-kilobyte FFT/scope buffers at 5–25 Hz). A useful network mirror of
  those is out of scope until the wire format is documented and the
  bandwidth implications are understood. v1 exposes mode-switching only —
  the operator can drive the on-LCD scope/spectrum from any client.
- Authentication / per-user accounts — LAN-only deployment, rely on network
  trust.
- Historical telemetry storage, charting, alerting.

## 3. Architecture

```
   ┌─────────────────────┐
   │    LP-500 / LP-700  │  USB HID, vendor-specific class, 64-byte reports
   └──────────┬──────────┘
              │ /dev/hidraw*  (Linux)   |   IOHIDDevice (macOS)   |   HID#... (Windows)
   ┌──────────▼──────────────────────────────────────────────┐
   │  lp700-server  (Go, single static binary + libudev)     │
   │                                                          │
   │   ┌──────────────┐    ┌──────────────┐    ┌───────────┐  │
   │   │  HID owner   │───►│  State hub   │───►│  WS hub   │  │
   │   │ (poll loop + │◄───│  (last-known │◄───│ (fan-out, │  │
   │   │   decoder)   │    │   snapshot)  │    │  fan-in)  │  │
   │   └──────────────┘    └──────────────┘    └─────┬─────┘  │
   └──────────────────────────────────────────────┬─┴────────┘
                                                  │  WebSocket /ws
              ┌────────────────────────┬──────────┼──────────┐
              ▼                        ▼          ▼          ▼
        Web dashboard           Logging PC     Phone     Station PC
```

**Three goroutines, one shared state:**

1. **HID owner** — opens the configured HID device, runs a read-loop that
   blocks on `device.Read()`, and a write-loop that drains a control queue.
   Decodes IN reports into a `Snapshot` struct via the working hypothesis
   in `internal/lpmeter/decode.go`. Owns a single-writer command queue:
   control verbs submitted by clients are written as 64-byte OUT reports
   between IN-report arrivals so we never collide on the wire.
2. **State hub** — keeps the most recent `Snapshot` plus a monotonic `seq`
   counter. Diffs each new snapshot against the previous and emits a
   `telemetry` frame when any field changes (with a heartbeat every N seconds
   even when idle, so clients can detect a dead server).
3. **WS hub** — accepts connections at `/ws`, sends each new client the
   current snapshot immediately (so a freshly opened tab is correct without
   waiting for the next change), then streams events. Inbound control
   messages are validated and forwarded to the HID owner's command queue.

**Why this shape:** the HID owner is the single writer to the device and the
single reader from it; the state hub is the only place the canonical
snapshot lives; the WS hub is stateless w.r.t. the meter. Clients are
decoupled from the poll cadence and from each other — one slow client cannot
stall the HID loop, and a new connection is warm immediately. Same shape as
`LP-100A-Server`, with the serial layer swapped for HID.

## 4. Wire protocol (WebSocket, JSON)

All frames are JSON text. A `type` discriminator distinguishes message kinds
and a monotonic `seq` on server→client frames lets clients detect drops.

**Server → client**

```json
// Sent immediately on connect, then on every changed field.
{
  "type": "telemetry",
  "seq": 12345,
  "ts": "2026-05-08T18:22:01.103Z",
  "data": {
    "channel": 2,
    "auto_channel": false,
    "power_avg_w": 94.9,
    "power_peak_w": 131.0,
    "peak_hold_w": 130,
    "swr": 1.06,
    "range": "100W",
    "peak_mode": "peak_hold",     // average | peak_hold | tune
    "power_mode": "net",          // net | delivered | forward
    "alarm_enabled": true,
    "alarm_power_w": 1500,
    "alarm_swr": 2.0,
    "alarm_tripped": false,
    "callsign": "N8LP",
    "coupler": "LPC501",
    "top_mode": "power_swr",      // power_swr | waveform | spectrum | setup
    "firmware_rev": "v2.5.2b4"
  }
}

// Heartbeat when nothing has changed for >2s.
{ "type": "heartbeat", "seq": 12346, "ts": "..." }

// HID errors, parse errors, etc.
{ "type": "status", "level": "warn", "msg": "hid reopened after 1.3s gap" }

// Reply to a client command.
{ "type": "ack", "ref": "client-supplied-id", "ok": true }
```

**Client → server**

```json
{ "type": "command", "id": "abc-1", "action": "mode_step" }
{ "type": "command", "id": "abc-2", "action": "channel_step", "value": 3 }
{ "type": "command", "id": "abc-3", "action": "range_step",   "value": 5 }
{ "type": "command", "id": "abc-4", "action": "alarm_toggle", "value": 1 }
{ "type": "command", "id": "abc-5", "action": "peak_toggle",  "value": 1 }
{ "type": "command", "id": "abc-6", "action": "setup_enter" }
{ "type": "command", "id": "abc-7", "action": "setup_exit"  }
{ "type": "command", "id": "abc-8", "action": "power_mode",   "value": 0 }

// Optional: ask the server to re-emit the current snapshot.
{ "type": "resync" }
```

The server intentionally does not expose raw HID reports. Clients speak in
named verbs; the server is the only thing that knows about the on-the-wire
report bytes. This keeps the protocol stable if the meter firmware changes.

## 5. Configuration

Single config file (TOML); CLI flags `-config <path>`, `-backend
<hid|simulator>`, and `-v` (verbose start).

```toml
[meter]
# Backend: "hid" (real device) or "simulator" (synthesised telemetry for
# development without a meter). "auto" = "hid" if a matching device is
# present, otherwise "simulator".
backend     = "auto"
vendor_id   = 0x04D8  # Microchip; 0 = fall back to product-string matching
product_id  = 0x0001  # PIC32 firmware default
poll_ms     = 40      # 25 Hz; the host actively polls with the '0' command

[server]
listen        = "0.0.0.0:8089"   # 8089 to avoid clashing with LP-100A on 8088
heartbeat_ms  = 2000
max_clients   = 32
allow_control = true

[ui]
title = "LP-500 / LP-700"
```

The default log level is **error** so the systemd journal stays quiet on a
Pi. Use `-v` to start at debug, or flip it at runtime via `POST
/api/log-level` (the web client's SETUP overlay has a picker for this).

## 6. Reference web client (v1)

A static HTML/JS page served from the same Go binary at `/`, embedded via
`go:embed` so the deployable artifact stays a single file. Mirrors the
LP-500/700 **Power/SWR** screen:

- Power readouts (Avg + Peak) with the W / kW unit auto-scaled.
- SWR readout, large.
- Range bar graph with the active range highlighted.
- Channel pills (Auto / 1 / 2 / 3 / 4) — tappable to send `channel_step`.
- Range button — taps `range_step` to advance.
- Alarm pill — taps `alarm_toggle`. Goes red when tripped.
- Peak/Avg/Tune button — taps `peak_toggle`.
- Top-mode pill (Power-SWR / Wfm / Spec / Setup) — taps `mode_step`.
- Connection-state pill (green/yellow/red) driven by heartbeat + `status`
  frames.
- **SETUP overlay** with the runtime log-level picker (read/write
  `/api/log-level`) and a backend indicator (so it's obvious when the page
  is showing simulated data).

## 7. Failure modes

| Failure                          | Behaviour                                            |
|----------------------------------|------------------------------------------------------|
| HID device disappears (USB pull) | Emit `status` warn, reconnect with backoff (1s→30s). Clients see heartbeats only. |
| Bad/short HID frame              | Drop frame, increment a parse-error counter, do not disconnect. |
| Decoder hypothesis mismatches    | Decoder sets `Snapshot.Valid = false`; hub does not broadcast invalid snapshots. The journal carries a warn with the raw bytes. Run `lp700-server probe -dump` and adjust `decode.go`. |
| Client sends malformed JSON      | Send `ack ok:false` if `id` known; otherwise close with code 1003. |
| >`max_clients` connect           | Reject with 503 + reason; existing clients unaffected. |
| Two clients press *Mode* at once | Both commands queued FIFO; both `ack`s sent; one telemetry update reflects the final state. No locking visible to clients. |

## 8. Milestones

1. **M1 — skeleton + simulator** ✅: Go binary builds on macOS/Linux, the
   simulator backend produces synthetic frames, the WS hub fans out, the
   reference web client renders. No hardware needed.
2. **M2 — HID enumeration + raw read** : `lp700-server probe -list` shows
   real LP-500/700 devices; `probe -dump` prints raw frames. The decoder
   continues to mark frames `Valid = false` until the layout in `decode.go`
   matches what the device emits.
3. **M3 — decoder reconciliation** : adjust `decode.go` against a captured
   fixture set; ship a `decode_test.go` that's keyed on real frames; flip
   `auto` backend to prefer `hid` when a match is found.
4. **M4 — control verbs verified** : test plan in §11. Every control verb
   is verified against a real meter; `peak_toggle` / `mode_step` /
   `channel_step` / `range_step` round-trip.
5. **M5 — Pi deployment hardening** : auto-reconnect, `/healthz`, TOML
   config + `-v` flag, runtime log-level via `/api/log-level`, ARM64
   cross-compile via `deploy/build-pi.sh`, systemd unit + udev rule +
   `install.sh` / `redeploy.sh`.

v1 = M1 + M5. The HID layer ships as a working hypothesis with simulator
fallback. M2–M4 require a real device and will land as follow-ups once the
maintainer has bench time with a meter and a USB sniffer.

## 9. Open questions / risks

- **HID frame layout (low)** — grounded in the KD4Z Node-RED flow. The
  Power/SWR fields the flow decodes (channel, auto-channel, range, top
  mode, average power, peak power, SWR, power multiplier) are read by
  this server with the same offsets and scaling. Alarm thresholds,
  peak/avg/tune mode, callsign, coupler model, and firmware revision
  are not yet decoded from real frames; the simulator populates them so
  the wire shape is stable for clients.
- **Control verb opcodes (medium)** — `'0'` poll, `'8'` channel step,
  `'9'` range step are confirmed by the Node-RED flow. The other five
  (`mode_step`, `alarm_toggle`, `peak_toggle`, `setup_enter`,
  `setup_exit`, `power_mode`) use plausible single-char codes from the
  remaining ASCII range and need bench validation. Mitigation: these
  are exposed as named verbs over WebSocket, so the wire bytes can be
  changed in `decode.go` without a client-side break.
- **Multi-meter support** — still one meter per server process. The
  callsign and firmware-rev fields are reported in telemetry but not used
  for routing; if multi-meter becomes a need, run multiple service
  instances on different ports.
- **Read-only mode** — shipped as `server.allow_control = false`. When set,
  control verbs are NACKed with `"control disabled"`.
- **Cross-compile cost** — `karalabe/hid` is CGO. The release workflow
  installs a Linux ARM64/ARMv7 cross gcc on the runner. Native builds on
  the Pi work with `apt-get install libudev-dev libusb-1.0-0-dev`.

## 10. Out of scope, explicitly

- Cloud relay / NAT traversal. If users want this off-LAN they front the
  server with a VPN (Tailscale, WireGuard) — we will not bake one in.
- Logging/charting. A separate consumer can subscribe to `/ws` and write to
  InfluxDB or similar; that's a different program.
- Replacing the Telepost VM on Windows. The vendor app stays usable; our
  server just needs the HID handle when it's running.
- Mirroring the **Waveform/'Scope** and **Spectrum** display modes. The
  protocol bandwidth is significantly larger and the wire format is even
  less documented than Power/SWR. The control verb `mode_step` lets clients
  *switch* the meter into those modes (so the on-LCD view is usable from
  the dashboard area), but the network mirror is Power/SWR-only.

## 11. Hardware-bring-up test plan

Run on a Pi (or any host) with a real LP-500/700 connected:

```sh
# 1. Verify enumeration. Expect to see one entry whose product string
#    contains "LP-500" or "LP-700".
sudo lp700-server probe -list

# 2. Capture a 10-second window of IN reports. The unique-frame de-dup is
#    just to keep the file small; field bytes that don't change between
#    polls (like callsign and coupler) get one row, channel-power-SWR get
#    many.
sudo lp700-server probe -capture lp500-fixture.bin -duration 10s

# 3. Compare against the hypothesis. The decoder will print byte offsets
#    for every field it thinks it knows; eyeball them against the on-LCD
#    readings. Adjust internal/lpmeter/decode.go and re-run.
sudo lp700-server probe -dump

# 4. Verify each control verb round-trips. With the binary running as a
#    server (sudo lp700-server -config ./config.toml) and the web UI open
#    in a browser, click each control button and confirm the meter's LCD
#    advances exactly one step. The verbs to verify, in order:
#    mode_step, channel_step (1..4 + Auto), range_step (Auto..10K),
#    alarm_toggle, peak_toggle (Average/Peak Hold/Tune), setup_enter,
#    setup_exit, power_mode (Net/Delivered/Forward).
```

If any verb misbehaves, log the OUT bytes from `/api/log-level=debug` and
adjust the OUT-report opcodes in `internal/lpmeter/owner.go`. Every step is
isolated to one file.
