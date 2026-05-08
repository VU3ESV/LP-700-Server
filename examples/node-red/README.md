# Node-RED via the lp700-server WebSocket gateway

Drop-in Node-RED flow that mirrors the LP-500/700 telemetry over the
`lp700-server` WebSocket endpoint instead of opening `/dev/hidraw*`
directly. The output `msg.payload` shape is intentionally identical to
KD4Z's `LP Dice and Slice` reference flow, so any downstream nodes
from that flow (Peak-Hold logic, dashboard gauges, range-label
look-ups, channel-label look-ups, etc.) keep working unchanged after
migration.

**Why migrate.** Direct USB-HID access from Node-RED needs
`node-red-contrib-usbhid` plus root or `plugdev` permissions, and only
one process at a time can hold the HID handle. Once `lp700-server` is
running on the Pi, *it* owns the meter, and Node-RED becomes a normal
TCP/WebSocket client — same model that the embedded web client and any
phone/dashboard uses. Multiple Node-RED instances or browser tabs can
read at once and any of them can issue control verbs.

## What's in here

```
examples/node-red/
├── lp700-websocket-flow.json   ← importable flow (18 nodes, one tab)
└── README.md                   ← this file
```

## Importing

1. In Node-RED's hamburger menu pick **Import → select a file** and load
   `lp700-websocket-flow.json`. Choose **Import to → new flow** so it
   lands on a separate tab named *LP-700 WebSocket* (the IDs are
   namespaced `lp7…` and won't collide with anything you already have).
2. Open the **`ws://lp700-server/ws`** node. Edit the websocket-client
   config and change the URL to point at your Pi:
   ```
   ws://raspberrypi.local:8089/ws
   ```
   (or whatever host you used in `redeploy.sh`). Leave **Send/Receive
   payload** at *payload as message*. Save.
3. Click **Deploy**. The *Connection state* function node should turn
   green within a couple of seconds — that's the heartbeat fan-out from
   the server confirming the link is up.

The flow exposes a single link-out node named **`lp700-telemetry`**.
Add a `link in` somewhere in your existing flow with the same name and
wire it into the first node of your old `LP Dice and Slice → Peak Hold
Logic → …` chain. The `msg.payload` keys match (see below).

## Migrating from KD4Z's HID-Direct flow

If you're coming from KD4Z's *LP-500/700 HID DIRECT v1.2* flow on tab
`a65f1d125d17f35a`, the migration is delete-3 / wire-1:

| Action | Nodes affected                                                |
|--------|---------------------------------------------------------------|
| Delete | `getHIDdevices`, `HIDdevice (HID-LP)`, `HIDConfig (LP-500-LP-700)` — the direct USB plumbing |
| Delete | `Poll Meter Values`, `LP Dice and Slice` — replaced by the new tab |
| Delete | `Peak Reset Command`, `Change Channel`, `Change Range` — replaced by the inject buttons on the new tab (or you can keep yours and rewire them to the new `ws://lp700-server/ws` *out* node, with `msg.payload` set to the JSON command string — see below) |
| Keep   | Everything downstream: `Peak Hold Logic`, `Set CH Label`, `Set Range Label`, `to KW if needed`, `Zero meters`, `Meter Range`, all dashboard nodes |
| Add    | One `link in` node named `lp700-telemetry` feeding into the head of the kept chain |

## Output shape (link-out: `lp700-telemetry`)

```js
msg.payload = {
  power_avg:  "94",        // string, watts, toFixed(0)   — same as KD4Z
  power_peak: "131",       // string, watts, toFixed(0)
  swr:        "1.06",      // string, toFixed(2), floor 1.00
  scale:      100,         // number, full-scale watts for active range
  mode:       0,           // 0=Power/SWR, 1=Waveform, 2=Spectrum, 3=Setup
  channel:    0,           // 0=Auto, 1..4 = manual channel
  range:      4            // 0..10 = 5W..10K, 11 = Auto
}
msg.parsed = {
  // The full lp700-server frame, friendlier strings ("100W", "peak_hold"),
  // for any new node you add. KD4Z's flow ignores this.
  channel: 1, auto_channel: true, power_avg_w: 94.9, power_peak_w: 131.0,
  swr: 1.06, range: "100W", peak_mode: "peak_hold", alarm_enabled: true,
  alarm_tripped: false, top_mode: "power_swr", status_message: "", …
}
msg.seq = 12345        // monotonic per-frame sequence (drop detector)
msg.ts  = "2026-05-08T18:22:01.103Z"
```

## Sending control verbs

The flow includes seven `inject` buttons that emit JSON command
frames. Click a button (or trigger it from your dashboard via a
`ui_button` node wired to the same websocket-out) and the meter steps
once:

| Button                    | Verb            | Effect                                           |
|---------------------------|-----------------|--------------------------------------------------|
| Peak / Avg / Tune cycle   | `peak_toggle`   | Cycles the meter's large numeric display mode    |
| Channel step              | `channel_step`  | Auto → CH 1 → CH 2 → CH 3 → CH 4 → Auto          |
| Range step                | `range_step`    | 5W → 10W → … → 10K → Auto → 5W                   |
| Alarm toggle              | `alarm_toggle`  | Flips alarm enable on/off                        |
| Mode step (LCD top mode)  | `mode_step`     | Power/SWR → Waveform → Spectrum → Setup → …      |
| Setup toggle              | `setup`         | Enters/exits the Setup screen                    |
| Freeze                    | `freeze`        | Freezes the Waveform / Spectrum trace            |

To send from your own node, make `msg.payload` a string like
`{"type":"command","id":"my-id","action":"peak_toggle"}` and wire it
into the **`ws://lp700-server/ws`** *out* node. The server replies on
the inbound side with `{"type":"ack","ref":"my-id","ok":true}`, which
the Parse-frame function routes to the **ack** debug node — handy for
seeing why a verb was rejected (`control disabled` / `command queue
full or unknown action`).

## Wire diagram

```
                     ┌─────────────────────────────────────────────────────┐
                     │            tab: LP-700 WebSocket                    │
                     │                                                     │
  meter ↔ Pi (USB)   │   ws://lp700-server/ws                              │
                     │   ┌────────────┐    ┌────────────┐    ┌──────────┐  │
   lp700-server ───── ──►│ ws in      │───►│ Parse      │───►│ Reshape  │──┼──→ link out
   running on Pi     │   │            │    │ frame      │    │ (KD4Z-   │  │   "lp700-telemetry"
                     │   └────────────┘    │            │    │  compat) │  │
                     │                     │ ├──────────┤    └──────────┘  │
                     │                     │ │heartbeat │ ─→ Connection    │
                     │                     │ │ack       │    state (status)│
                     │                     │ └──────────┘ ─→ debug "ack"   │
                     │                                                     │
                     │   ┌────────────┐    ┌────────────┐                  │
                     │   │ inject ×7  │───►│ ws out     │                  │
                     │   │ (verbs)    │    │            │                  │
                     │   └────────────┘    └────────────┘                  │
                     └─────────────────────────────────────────────────────┘
```

## Troubleshooting

| Symptom                                  | Likely cause / fix                                                                                                                                  |
|------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------|
| Connection state node stays grey/red     | Wrong URL on the websocket-client config, or the Pi isn't reachable. Verify with `curl http://<host>:8089/healthz` from the Node-RED host.          |
| Telemetry frames don't arrive            | Open the *Parse frame* function's debug pane (route output 1 to a debug node temporarily). If frames arrive but `frame.type !== 'telemetry'`, the server is sending only heartbeats — check that the meter is enumerated (`lp700-server probe -list` on the Pi). |
| Buttons don't move the meter             | Either `server.allow_control = false` on the Pi (read-only port), or the meter is in Setup mode and ignoring soft input. The *ack* debug node will say `control disabled` in the first case. |
| Two flows fight for the meter            | They don't — that's the whole point of the gateway. Both can subscribe simultaneously, both can send commands; the server's single-writer queue serialises writes FIFO. |

## See also

- [README.md](../../README.md) — install / deploy the lp700-server.
- [ARCHITECTURE.md §4](../../ARCHITECTURE.md) — full WebSocket wire protocol.
- [MANUAL.md](../../MANUAL.md) — operator manual for the embedded web
  client (same /ws endpoint).
- `.support/links.txt` — KD4Z's original Node-RED flow URL and the
  Telepost vendor archive these decoders were grounded against.
