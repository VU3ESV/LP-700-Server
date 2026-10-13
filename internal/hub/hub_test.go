package hub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"lp700-server/internal/lpmeter"
)

// fakeSource records the verbs that reach Submit.
type fakeSource struct{ submitted []string }

func (f *fakeSource) Run(ctx context.Context) {}
func (f *fakeSource) Submit(verb string, value int) bool {
	f.submitted = append(f.submitted, verb)
	return true
}
func (f *fakeSource) Backend() lpmeter.Backend { return lpmeter.BackendSimulator }

type ackFrame struct {
	Type   string `json:"type"`
	Ref    string `json:"ref"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}

// TestCommandGatedInCHAuto checks the hub NACKs range_step / alarm_toggle
// while the latest snapshot is on CH Auto, without queueing a HID write,
// and lets the same verbs through on a manual channel.
func TestCommandGatedInCHAuto(t *testing.T) {
	tests := []struct {
		name       string
		verb       string
		auto       bool
		wantOK     bool
		wantSubmit bool
	}{
		{"range_step NACKed in CH Auto", "range_step", true, false, false},
		{"alarm_toggle NACKed in CH Auto", "alarm_toggle", true, false, false},
		{"channel_step passes in CH Auto", "channel_step", true, true, true},
		{"range_step passes on CH 2", "range_step", false, true, true},
		{"alarm_toggle passes on CH 2", "alarm_toggle", false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &fakeSource{}
			h := NewHub(nil, nil, nil, src, Options{AllowControl: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			h.latestSnap.Store(&lpmeter.Snapshot{Valid: true, AutoChannel: tt.auto, Channel: 2})
			c := &client{send: make(chan []byte, 1)}

			h.handleClientMsg(c, []byte(`{"type":"command","id":"t1","action":"`+tt.verb+`"}`))

			var ack ackFrame
			if err := json.Unmarshal(<-c.send, &ack); err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			if ack.Type != "ack" || ack.Ref != "t1" || ack.OK != tt.wantOK {
				t.Errorf("ack = %+v, want type=ack ref=t1 ok=%v", ack, tt.wantOK)
			}
			if !tt.wantOK && !strings.Contains(ack.Reason, "CH Auto") {
				t.Errorf("NACK reason %q should mention CH Auto", ack.Reason)
			}
			if got := len(src.submitted) == 1; got != tt.wantSubmit {
				t.Errorf("submitted = %v, want submit=%v", src.submitted, tt.wantSubmit)
			}
		})
	}
}
