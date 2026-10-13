package lpmeter

import "testing"

// TestSimulatorRangeAlarmFrozenInCHAuto checks the simulator matches the
// firmware: range_step / alarm_toggle change nothing while on CH Auto,
// and work again once a manual channel is selected.
func TestSimulatorRangeAlarmFrozenInCHAuto(t *testing.T) {
	s := Snapshot{AutoChannel: true, Channel: 1, Range: "100W", AlarmEnabled: true}
	applyCommand(&s, command{verb: "range_step", value: -1})
	applyCommand(&s, command{verb: "alarm_toggle", value: -1})
	if s.Range != "100W" || !s.AlarmEnabled {
		t.Errorf("CH Auto: range=%q alarm=%v, want unchanged 100W/true", s.Range, s.AlarmEnabled)
	}

	applyCommand(&s, command{verb: "channel_step", value: 2})
	applyCommand(&s, command{verb: "range_step", value: -1})
	applyCommand(&s, command{verb: "alarm_toggle", value: -1})
	if s.Range == "100W" || s.AlarmEnabled {
		t.Errorf("CH 2: range=%q alarm=%v, want range stepped and alarm off", s.Range, s.AlarmEnabled)
	}
}
