package lpmeter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ReportSize is the fixed length of every IN/OUT report on the LP-500/700.
// The mikroElektronika USB HID stack defaults to 64-byte vendor reports,
// confirmed by the KD4Z Node-RED flow (`.support/LP700NodeRed flow.json`)
// which polls and parses 64-byte buffers verbatim.
const ReportSize = 64

// LP-500/700 Microchip USB IDs. The PIC32 firmware uses the default
// Microchip VID and product 0x0001 (`vid: "1240" pid: "1"` in the
// Node-RED flow's HIDConfig — the literals are decimal: 1240 = 0x04D8).
const (
	DefaultVendorID  uint16 = 0x04D8
	DefaultProductID uint16 = 0x0001
)

// Single-byte ASCII control codes the meter accepts at OUT-report byte[1].
// Confirmed by the Node-RED flow:
//
//	'0' (0x30) — poll: ask for a fresh telemetry frame
//	'8' (0x38) — change channel (advances to the next of CH1..CH4..Auto)
//	'9' (0x39) — change range  (advances to the next range step)
//
// The remaining verbs below are a working hypothesis based on the
// front-panel button order and the convention that single-char ASCII
// commands map to button presses. They are exercised by the
// hardware-bring-up test plan in ARCHITECTURE.md §11.
const (
	cmdPoll        byte = '0'
	cmdMode        byte = '1'
	cmdAlarm       byte = '2'
	cmdPeak        byte = '3'
	cmdSetupEnter  byte = '4'
	cmdSetupExit   byte = '5'
	cmdPowerMode   byte = '6'
	cmdAlarmToggle byte = '7'
	cmdChannel     byte = '8'
	cmdRange       byte = '9'
)

// IN-report byte offsets (Node-RED-derived; see CLAUDE.md and the
// `LP Dice and Slice` function in `.support/LP700NodeRed flow.json`).
const (
	OffsetSWRHi       = 2 // big-endian, split with OffsetSWRLo
	OffsetTopMode     = 3
	OffsetChannel     = 4
	OffsetChannelAuto = 5
	OffsetRange       = 6
	OffsetPeakPwrHi   = 23
	OffsetPeakPwrLo   = 24
	OffsetAvgPwrHi    = 25
	OffsetAvgPwrLo    = 26
	OffsetPwrMultHi   = 30
	OffsetPwrMultLo   = 31
	OffsetSWRLo       = 37
)

// PollReport returns the 64-byte OUT report payload that asks the meter
// for a fresh telemetry frame. The HID owner sends this every tick.
func PollReport() []byte {
	out := make([]byte, ReportSize)
	out[1] = cmdPoll
	return out
}

// Decode parses a 64-byte IN report from the LP-500/700 into a Snapshot.
//
// The byte layout is grounded in the KD4Z Node-RED flow that's known to
// work against real hardware (see CLAUDE.md). Fields whose offsets are
// not yet known (alarm thresholds, callsign, coupler model, firmware
// revision) are left at their zero value; they're populated by the
// simulator backend so the wire shape remains stable for clients.
func Decode(report []byte) (Snapshot, error) {
	if len(report) != ReportSize {
		return Snapshot{}, fmt.Errorf("expected %d-byte report, got %d", ReportSize, len(report))
	}

	s := Snapshot{Timestamp: time.Now().UTC()}

	// Power: big-endian unsigned 16-bit, scale = ×0.2 W (= raw * 2 / 10
	// per the Node-RED flow). 0x0000 is the meter idle / no-RF state.
	rawPeak := binary.BigEndian.Uint16(report[OffsetPeakPwrHi : OffsetPeakPwrHi+2])
	rawAvg := binary.BigEndian.Uint16(report[OffsetAvgPwrHi : OffsetAvgPwrHi+2])
	s.PowerPeakW = float64(rawPeak) * 0.2
	s.PowerAvgW = float64(rawAvg) * 0.2

	// SWR: big-endian split bytes (hi at offset 2, lo at offset 37),
	// scale = / 100. SWR < 1.00 is impossible; treat 0 as "not measured"
	// rather than rejecting the frame.
	rawSWR := uint16(report[OffsetSWRHi])<<8 | uint16(report[OffsetSWRLo])
	s.SWR = float64(rawSWR) / 100.0
	if s.SWR < 1.0 {
		s.SWR = 1.0
	}

	// Power multiplier: encodes the active full-scale watts. scale =
	// (mult / 4) per the Node-RED Meter-Range function. Useful for
	// driving a bargraph; we expose it as the Range string.
	rawMult := binary.BigEndian.Uint16(report[OffsetPwrMultHi : OffsetPwrMultHi+2])
	scale := int(rawMult / 2) // == (rawMult * 2) / 4
	_ = scale                 // kept for future bargraph exposure

	// Channel byte: 0=Auto, 1..4=CH1..CH4. The separate ChannelAuto byte
	// signals which physical channel the auto-mode is currently locked
	// to (so we keep both interpretations).
	chByte := report[OffsetChannel]
	chAutoByte := report[OffsetChannelAuto]
	switch {
	case chByte == 0:
		s.AutoChannel = true
		// When auto, ChannelAuto carries the locked physical channel.
		if chAutoByte >= 1 && chAutoByte <= 4 {
			s.Channel = int(chAutoByte)
		} else {
			s.Channel = 1
		}
	case chByte >= 1 && chByte <= 4:
		s.AutoChannel = false
		s.Channel = int(chByte)
	default:
		return s, fmt.Errorf("channel byte 0x%02x out of range", chByte)
	}

	// Range byte: 0..10 = 5W..10KW, 11 = Auto.
	rng := report[OffsetRange]
	if int(rng) >= len(rangeNames) {
		return s, fmt.Errorf("range byte 0x%02x out of range", rng)
	}
	s.Range = rangeNames[rng]

	// Top-level mode: hypothesis. The Node-RED flow stores `mode` but
	// doesn't decode it; the user-guide page 4 lists the cycle as
	// Power/SWR → Waveform → Spectrum (Setup is reachable from the
	// Setup button rather than the Mode button cycle).
	if int(report[OffsetTopMode]) < len(topModeNames) {
		s.TopMode = topModeNames[report[OffsetTopMode]]
	} else {
		s.TopMode = "power_swr"
	}

	s.Valid = true
	return s, nil
}

// errSkipFrame signals to the caller that the report was structurally
// valid but is not a Power/SWR telemetry frame and should be ignored.
var errSkipFrame = errors.New("non-telemetry frame, skipping")

// IsSkippable returns true for the sentinel errSkipFrame so callers can
// distinguish "frame was a different type, that's fine" from "frame was
// corrupt".
func IsSkippable(err error) bool { return errors.Is(err, errSkipFrame) }

// KnownVerbs is the set of control-verb names the server accepts on the
// WebSocket. Used by the hub to NACK unknown verbs before they reach the
// HID write queue. Keep in sync with EncodeCommand.
var KnownVerbs = map[string]bool{
	"mode_step":    true,
	"channel_step": true,
	"range_step":   true,
	"alarm_toggle": true,
	"peak_toggle":  true,
	"setup_enter":  true,
	"setup_exit":   true,
	"power_mode":   true,
}

// EncodeCommand renders a 64-byte OUT report payload for a single named
// control verb. The on-the-wire convention (byte[0]=0 sync, byte[1]=ASCII
// command) is taken from the KD4Z Node-RED flow's `Poll Meter Values`,
// `Change Channel`, and `Change Range` functions (commands '0', '8',
// '9'). The other verbs use plausible single-char codes that need
// hardware verification — see ARCHITECTURE.md §11.
//
// The `value` parameter is currently unused on the wire (the meter's
// commands all advance the relevant state by one step), but the verb
// surface keeps it so a future "set channel directly to N" command can
// land without a client API break.
func EncodeCommand(verb string, value int) ([]byte, error) {
	out := make([]byte, ReportSize)
	switch verb {
	case "mode_step":
		out[1] = cmdMode
	case "channel_step":
		out[1] = cmdChannel
	case "range_step":
		out[1] = cmdRange
	case "alarm_toggle":
		out[1] = cmdAlarmToggle
	case "peak_toggle":
		out[1] = cmdPeak
	case "setup_enter":
		out[1] = cmdSetupEnter
	case "setup_exit":
		out[1] = cmdSetupExit
	case "power_mode":
		out[1] = cmdPowerMode
	default:
		return nil, fmt.Errorf("unknown verb %q", verb)
	}
	_ = value
	return out, nil
}
