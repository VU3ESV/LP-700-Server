// Package lpmeter implements the LP-500 / LP-700 USB HID link layer:
// the snapshot model, the frame decoder grounded in the KD4Z Node-RED
// reference flow, the HID owner goroutine that arbitrates reads and
// writes, the simulator backend, and the diagnostic probe used to
// capture real frames for fixture creation.
//
// See CLAUDE.md and ARCHITECTURE.md for protocol references and bring-up
// notes.
package lpmeter

import "time"

// Snapshot is the parsed state of the LP-500/700 at a single poll instant.
// JSON tags define the wire shape sent to clients.
//
// Fields the wire decoder fills in from real frames (Node-RED-grounded):
//   - Channel, AutoChannel, PowerAvgW, PowerPeakW, SWR, Range, TopMode
//
// Fields the simulator fills in but the wire decoder leaves at zero
// (offsets not yet known — see CLAUDE.md):
//   - PeakHoldW, PeakMode, PowerMode, AlarmEnabled, AlarmPowerW,
//     AlarmSWR, AlarmTripped, Callsign, Coupler, FirmwareRev
//
// The wire shape is stable: clients always see the same JSON keys
// whether they're connected to a real meter or the simulator. They just
// see zero/empty values for fields the real decoder hasn't grown yet.
type Snapshot struct {
	Timestamp time.Time `json:"-"`

	// Power/SWR mode telemetry.
	Channel     int     `json:"channel"`      // 1..4
	AutoChannel bool    `json:"auto_channel"` // CH Auto active
	PowerAvgW   float64 `json:"power_avg_w"`
	PowerPeakW  float64 `json:"power_peak_w"`
	PeakHoldW   float64 `json:"peak_hold_w"`
	SWR         float64 `json:"swr"`

	// Mode controls.
	Range     string `json:"range"`      // 5W | 10W | … | 10K | auto
	PeakMode  string `json:"peak_mode"`  // average | peak_hold | tune
	PowerMode string `json:"power_mode"` // net | delivered | forward

	// Alarms (per active channel).
	AlarmEnabled bool    `json:"alarm_enabled"`
	AlarmPowerW  float64 `json:"alarm_power_w"`
	AlarmSWR     float64 `json:"alarm_swr"`
	AlarmTripped bool    `json:"alarm_tripped"`

	// Setup / metadata.
	Callsign    string `json:"callsign"`
	Coupler     string `json:"coupler"`      // LPC501 | LPC502 | LPC503 | LPC504 | LPC505
	TopMode     string `json:"top_mode"`     // power_swr | waveform | spectrum | setup
	FirmwareRev string `json:"firmware_rev"` // e.g. "v2.5.2b4"

	// Valid is false for snapshots that the decoder produced but failed
	// internal sanity checks. The hub does not broadcast invalid
	// snapshots.
	Valid bool `json:"-"`
}

// CloseEnough returns true if two snapshots are equivalent for broadcast
// purposes. Per-field deadbands suppress float jitter so we don't fan
// out a frame on every polling cycle when the radio is keyed.
func (s Snapshot) CloseEnough(o Snapshot) bool {
	return s.Channel == o.Channel &&
		s.AutoChannel == o.AutoChannel &&
		floatNear(s.PowerAvgW, o.PowerAvgW, 0.5) &&
		floatNear(s.PowerPeakW, o.PowerPeakW, 0.5) &&
		floatNear(s.PeakHoldW, o.PeakHoldW, 0.5) &&
		floatNear(s.SWR, o.SWR, 0.005) &&
		s.Range == o.Range &&
		s.PeakMode == o.PeakMode &&
		s.PowerMode == o.PowerMode &&
		s.AlarmEnabled == o.AlarmEnabled &&
		floatNear(s.AlarmPowerW, o.AlarmPowerW, 0.5) &&
		floatNear(s.AlarmSWR, o.AlarmSWR, 0.005) &&
		s.AlarmTripped == o.AlarmTripped &&
		s.Callsign == o.Callsign &&
		s.Coupler == o.Coupler &&
		s.TopMode == o.TopMode &&
		s.FirmwareRev == o.FirmwareRev
}

func floatNear(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

// rangeNames is the wire-byte-to-label table for the Range field. The
// order matches the Node-RED flow's `Set Range Label` switch:
//   0=5W, 1=10W, 2=25W, 3=50W, 4=100W, 5=250W, 6=500W,
//   7=1KW, 8=2.5KW, 9=5KW, 10=10KW, 11=Auto
var rangeNames = []string{
	"5W", "10W", "25W", "50W", "100W", "250W", "500W", "1K", "2.5K", "5K", "10K", "auto",
}

// Peak-mode index ↔ name. Verified empirically on real hardware: the
// firmware encodes byte[8] as 0=Peak Hold, 1=Average, 2=Tune, matching
// the F5 soft-button cycle order in the LP-500 user guide page 4.
var peakModeNames = []string{"peak_hold", "average", "tune"}

// Power-mode index ↔ name (user-guide page 8 setup: Net (F-R) / Delivered (F+R) / Forward).
var powerModeNames = []string{"net", "delivered", "forward"}

// Coupler index ↔ part number (user-guide page 8).
var couplerNames = []string{"LPC501", "LPC502", "LPC503", "LPC504", "LPC505"}

// Top-level mode index ↔ name. Hypothesised order — see decode.go.
var topModeNames = []string{"power_swr", "waveform", "spectrum", "setup"}
