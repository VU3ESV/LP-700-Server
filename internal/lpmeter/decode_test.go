package lpmeter

import (
	"encoding/binary"
	"testing"
)

// buildSyntheticFrame produces a 64-byte report that matches the Node-RED-
// grounded layout in decode.go, so the round-trip can be exercised
// without hardware. When real fixtures are captured via
// `lp700-server probe -capture`, drop them in `testdata/` and add a
// golden test that reads the bytes verbatim.
func buildSyntheticFrame(s Snapshot) []byte {
	r := make([]byte, ReportSize)

	// Power: scale=×0.2W → raw = watts * 5 (== watts / 0.2).
	rawAvg := uint16(s.PowerAvgW / 0.2)
	rawPeak := uint16(s.PowerPeakW / 0.2)
	binary.BigEndian.PutUint16(r[OffsetAvgPwrHi:], rawAvg)
	binary.BigEndian.PutUint16(r[OffsetPeakPwrHi:], rawPeak)

	// SWR: split bytes, scale=/100 → raw = swr * 100.
	rawSWR := uint16(s.SWR * 100)
	r[OffsetSWRHi] = byte(rawSWR >> 8)
	r[OffsetSWRLo] = byte(rawSWR & 0xff)

	// Range
	if i := indexOf(rangeNames, s.Range); i >= 0 {
		r[OffsetRange] = byte(i)
	}

	// Channel
	if s.AutoChannel {
		r[OffsetChannel] = 0
		if s.Channel >= 1 && s.Channel <= 4 {
			r[OffsetChannelAuto] = byte(s.Channel)
		} else {
			r[OffsetChannelAuto] = 1
		}
	} else {
		r[OffsetChannel] = byte(s.Channel)
	}

	// Top mode
	if i := indexOf(topModeNames, s.TopMode); i >= 0 {
		r[OffsetTopMode] = byte(i)
	}

	// PowerMult ≈ scale * 4. The decoder doesn't surface scale
	// directly, but populating the multiplier keeps the frame
	// realistic for future tests that read it.
	if scale := scaleFromRange(s.Range); scale > 0 {
		mult := uint16(scale * 4)
		binary.BigEndian.PutUint16(r[OffsetPwrMultHi:], mult/2) // raw*2/4 = scale → raw = scale*2
	}
	return r
}

func scaleFromRange(name string) int {
	switch name {
	case "5W":
		return 5
	case "10W":
		return 10
	case "25W":
		return 25
	case "50W":
		return 50
	case "100W":
		return 100
	case "250W":
		return 250
	case "500W":
		return 500
	case "1K":
		return 1000
	case "2.5K":
		return 2500
	case "5K":
		return 5000
	case "10K":
		return 10000
	}
	return 0
}

func indexOf(table []string, v string) int {
	for i, s := range table {
		if s == v {
			return i
		}
	}
	return -1
}

func TestDecodeRoundTripWireFields(t *testing.T) {
	want := Snapshot{
		Channel:     2,
		AutoChannel: false,
		PowerAvgW:   100, // 100W round-trips exactly via the ×0.2 scale (raw=500)
		PowerPeakW:  140, // raw=700
		SWR:         1.06,
		Range:       "100W",
		TopMode:     "power_swr",
	}
	got, err := Decode(buildSyntheticFrame(want))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Valid {
		t.Fatal("decoded snapshot is marked invalid")
	}
	// We only assert on fields the decoder fills in from real frames.
	if got.Channel != want.Channel {
		t.Errorf("channel: got %d, want %d", got.Channel, want.Channel)
	}
	if got.AutoChannel != want.AutoChannel {
		t.Errorf("auto: got %v, want %v", got.AutoChannel, want.AutoChannel)
	}
	if !floatNear(got.PowerAvgW, want.PowerAvgW, 0.2) {
		t.Errorf("power avg: got %v, want %v", got.PowerAvgW, want.PowerAvgW)
	}
	if !floatNear(got.PowerPeakW, want.PowerPeakW, 0.2) {
		t.Errorf("power peak: got %v, want %v", got.PowerPeakW, want.PowerPeakW)
	}
	if !floatNear(got.SWR, want.SWR, 0.01) {
		t.Errorf("swr: got %v, want %v", got.SWR, want.SWR)
	}
	if got.Range != want.Range {
		t.Errorf("range: got %s, want %s", got.Range, want.Range)
	}
	if got.TopMode != want.TopMode {
		t.Errorf("top mode: got %s, want %s", got.TopMode, want.TopMode)
	}
}

func TestDecodeAutoChannel(t *testing.T) {
	got, err := Decode(buildSyntheticFrame(Snapshot{
		AutoChannel: true, Channel: 3,
		PowerAvgW: 50, PowerPeakW: 70, SWR: 1.10, Range: "100W",
	}))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.AutoChannel || got.Channel != 3 {
		t.Errorf("auto/channel: got auto=%v ch=%d, want auto=true ch=3", got.AutoChannel, got.Channel)
	}
}

func TestDecodeSWRFloorsAtOne(t *testing.T) {
	r := buildSyntheticFrame(Snapshot{Channel: 1, Range: "100W", SWR: 0.5})
	got, err := Decode(r)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SWR != 1.0 {
		t.Errorf("expected swr floor at 1.0 (raw < 100), got %v", got.SWR)
	}
}

func TestDecodeRejectsShortFrame(t *testing.T) {
	if _, err := Decode(make([]byte, ReportSize-1)); err == nil {
		t.Fatal("expected error for short frame")
	}
}

func TestDecodeRejectsBadChannel(t *testing.T) {
	r := buildSyntheticFrame(Snapshot{Channel: 1, Range: "100W"})
	r[OffsetChannel] = 7 // invalid
	if _, err := Decode(r); err == nil {
		t.Fatal("expected error for out-of-range channel byte")
	}
}

func TestDecodeRejectsBadRange(t *testing.T) {
	r := buildSyntheticFrame(Snapshot{Channel: 1, Range: "100W"})
	r[OffsetRange] = 99
	if _, err := Decode(r); err == nil {
		t.Fatal("expected error for out-of-range range byte")
	}
}

func TestEncodeCommandUnknownVerb(t *testing.T) {
	if _, err := EncodeCommand("nope", 0); err == nil {
		t.Fatal("expected error for unknown verb")
	}
}

func TestEncodeCommandLayout(t *testing.T) {
	// The poll command and the documented control bytes from the
	// Node-RED flow must land at byte 1 of the report.
	cases := map[string]byte{
		"channel_step": '8',
		"range_step":   '9',
		"mode_step":    '1',
		"alarm_toggle": '7',
		"peak_toggle":  '3',
		"setup_enter":  '4',
		"setup_exit":   '5',
		"power_mode":   '6',
	}
	for verb, want := range cases {
		out, err := EncodeCommand(verb, 0)
		if err != nil {
			t.Errorf("%s: %v", verb, err)
			continue
		}
		if len(out) != ReportSize {
			t.Errorf("%s: got %d-byte report, want %d", verb, len(out), ReportSize)
		}
		if out[1] != want {
			t.Errorf("%s: byte[1]=0x%02x, want 0x%02x", verb, out[1], want)
		}
		if out[0] != 0 {
			t.Errorf("%s: byte[0]=0x%02x, want 0 (sync)", verb, out[0])
		}
	}
}

func TestPollReportShape(t *testing.T) {
	p := PollReport()
	if len(p) != ReportSize {
		t.Fatalf("poll size %d, want %d", len(p), ReportSize)
	}
	if p[1] != '0' {
		t.Errorf("poll byte[1]=0x%02x, want '0' (0x30)", p[1])
	}
}
