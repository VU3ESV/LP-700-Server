package lpmeter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ReportSize is the fixed length of every IN/OUT report on the LP-500/700.
// 64-byte vendor reports, no Report ID — the wire format is documented in
// the manufacturer's `LP-500_DataLogger` VB6 source committed at
// `.support/LP-500_DataLogger/`. Karalabe-style writes still need a Report
// ID byte prefixed (writeReport in owner.go does that), but the wire-level
// report itself is 64 bytes.
const ReportSize = 64

// LP-500/700 Microchip USB IDs. Confirmed in two independent sources:
//   - .support/LP-500_DataLogger/USB_HID_Functions.bas:5-6
//     (`MyVendorID = &H4D8`, `MyProductID = &H1`)
//   - .support/LP700NodeRed flow.json HIDConfig (`vid: "1240"`, `pid: "1"`,
//     decimal: 1240 = 0x04D8).
const (
	DefaultVendorID  uint16 = 0x04D8
	DefaultProductID uint16 = 0x0001
)

// Wire-level command bytes the firmware accepts at byte 0 of an OUT
// report. Confirmed by `.support/LP-500_DataLogger/FrmSetup.frm`
// button handlers (`OutputReportData(1) = N` after the RID prefix at
// `OutputReportData(0)`).
const (
	cmdPoll    byte = '0' // 48 — poll / get fresh telemetry
	cmdMode    byte = '7' // 55 — F1 Mode
	cmdChannel byte = '8' // 56 — F2 CH
	cmdRange   byte = '9' // 57 — F3 Rng
	cmdAlarm   byte = ':' // 58 — F4 AL
	cmdPeak    byte = ';' // 59 — F5 Peak / Avg / Tune
	cmdSetup   byte = '<' // 60 — F6 Setup (toggles enter/exit)
	cmdFreeze  byte = '?' // 63 — Freeze (waveform/spectrum only)
)

// IN-report byte offsets, 0-based against the 64-byte buffer that Linux
// hidraw delivers on Read (no RID prefix). Taken from the DataLogger
// source's InputReportData(N) indices minus 1.
//
// Source: .support/LP-500_DataLogger/FrmSetup.frm:303-339.
const (
	OffsetSWRHi       = 2 // 16-bit BE, split with OffsetSWRLo (Node-RED-derived)
	OffsetTopMode     = 3 // 0=Power/SWR, 1=Waveform, 2=Spectrum, 3=Setup
	OffsetChannel     = 4 // 0=Auto, 1..4=CH1..CH4
	OffsetChannelAuto = 5 // when channel==0 (Auto), the physical channel auto is locked to (1..4)
	OffsetRange       = 6 // 0..10 = 5W..10KW, 11 = Auto
	OffsetAlarm       = 7 // 0=off, non-zero=on
	OffsetPeakAvg     = 8 // 0=peak_hold, 1=average, 2=tune
	OffsetAlarmSet    = 9 // alarm setpoint index (encoding TBD)
	OffsetPeakPwrHi   = 23
	OffsetPeakPwrLo   = 24
	OffsetAvgPwrHi    = 25
	OffsetAvgPwrLo    = 26
	OffsetBGAvg       = 27 // bargraph display scaling — average
	OffsetBGPk        = 28 // bargraph display scaling — peak
	OffsetBGSWR       = 29 // bargraph display scaling — SWR
	OffsetPwrMultHi   = 30 // active full-scale watts; scale = (raw / 2)
	OffsetPwrMultLo   = 31
	OffsetPeakAvgMode = 32 // Peak/Avg display mode (distinct from peak hold cycle)
	OffsetFilter      = 33
	OffsetFreeze      = 34
	OffsetSWRLo       = 37 // hypothesis from the older Node-RED parser
)

// PollReport returns the 64-byte OUT report payload that asks the meter
// for a fresh telemetry frame. Byte 0 = command character ('0'), rest is
// zero. The HID owner sends this every tick.
func PollReport() []byte {
	out := make([]byte, ReportSize)
	out[0] = cmdPoll
	return out
}

// Decode parses a 64-byte IN report from the LP-500/700 into a Snapshot.
// Layout grounded in the manufacturer's DataLogger source.
func Decode(report []byte) (Snapshot, error) {
	if len(report) != ReportSize {
		return Snapshot{}, fmt.Errorf("expected %d-byte report, got %d", ReportSize, len(report))
	}

	// The firmware emits an ack/echo frame after every OUT command:
	// byte[0] = the command character we wrote (e.g. 0x30 for poll),
	// byte[1..63] = 0. Real telemetry frames always have byte[0] = 0.
	// Skip the echoes — without this, the decoder conflates them with
	// telemetry and produces nonsense (alternating range=auto/5W in
	// what should be a steady-state log).
	if report[0] != 0 {
		return Snapshot{}, errSkipFrame
	}

	s := Snapshot{Timestamp: time.Now().UTC()}

	// Power: big-endian unsigned 16-bit, scale = ×0.2 W (raw * 2 / 10
	// per the Node-RED `LP Dice and Slice` function).
	rawPeak := binary.BigEndian.Uint16(report[OffsetPeakPwrHi : OffsetPeakPwrHi+2])
	rawAvg := binary.BigEndian.Uint16(report[OffsetAvgPwrHi : OffsetAvgPwrHi+2])
	s.PowerPeakW = float64(rawPeak) * 0.2
	s.PowerAvgW = float64(rawAvg) * 0.2

	// SWR: big-endian split bytes (hi at offset 2, lo at offset 37),
	// scaled /100. Floor at 1.0 — SWR < 1.00 is impossible.
	rawSWR := uint16(report[OffsetSWRHi])<<8 | uint16(report[OffsetSWRLo])
	s.SWR = float64(rawSWR) / 100.0
	if s.SWR < 1.0 {
		s.SWR = 1.0
	}

	// Channel byte: 0=Auto, 1..4=CH1..CH4. When in Auto, byte 5 carries
	// the physical channel auto-mode is currently locked to (1..4).
	chByte := report[OffsetChannel]
	chAutoByte := report[OffsetChannelAuto]
	switch {
	case chByte == 0:
		s.AutoChannel = true
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

	// Numeric alarm thresholds (Pwr W, SWR) live somewhere we haven't
	// pinned yet — likely served by a separate "get setup" command.
	// Leave them at zero so the UI shows — until we find them.

	// Range byte: 0..10 = 5W..10KW, 11 = Auto.
	rng := report[OffsetRange]
	if int(rng) >= len(rangeNames) {
		return s, fmt.Errorf("range byte 0x%02x out of range", rng)
	}
	s.Range = rangeNames[rng]

	// Top-level mode (0..3).
	if int(report[OffsetTopMode]) < len(topModeNames) {
		s.TopMode = topModeNames[report[OffsetTopMode]]
	} else {
		s.TopMode = "power_swr"
	}

	// Alarm enable + peak-mode are single bytes per the DataLogger map.
	s.AlarmEnabled = report[OffsetAlarm] != 0
	if int(report[OffsetPeakAvg]) < len(peakModeNames) {
		s.PeakMode = peakModeNames[report[OffsetPeakAvg]]
	}

	s.Valid = true
	return s, nil
}

// errSkipFrame signals that a structurally valid but non-telemetry frame
// should be ignored. Reserved for future use; the current single-frame-
// type protocol doesn't emit it.
var errSkipFrame = errors.New("non-telemetry frame, skipping")

// IsSkippable returns true for the sentinel errSkipFrame.
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
	"setup":        true,
	"freeze":       true,
}

// EncodeCommand renders a 64-byte OUT report payload for a single named
// control verb. Byte 0 is the command character; rest is zero. The
// `value` parameter is currently unused on the wire (the firmware's
// commands all advance by one step) but is preserved so a future "set
// channel directly to N" command can land without a client API break.
func EncodeCommand(verb string, value int) ([]byte, error) {
	out := make([]byte, ReportSize)
	switch verb {
	case "mode_step":
		out[0] = cmdMode
	case "channel_step":
		out[0] = cmdChannel
	case "range_step":
		out[0] = cmdRange
	case "alarm_toggle":
		out[0] = cmdAlarm
	case "peak_toggle":
		out[0] = cmdPeak
	case "setup":
		out[0] = cmdSetup
	case "freeze":
		out[0] = cmdFreeze
	default:
		return nil, fmt.Errorf("unknown verb %q", verb)
	}
	_ = value
	return out, nil
}
