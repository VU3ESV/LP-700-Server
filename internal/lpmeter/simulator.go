package lpmeter

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"time"
)

// Simulator is a Source that synthesises a believable LP-500/700 telemetry
// stream so the WebSocket hub, the web client, and any third-party clients
// can be developed and demoed without a real meter present. It walks
// through the range and peak-mode cycles, raises the alarm periodically,
// and modulates power/SWR with smooth jitter.
type Simulator struct {
	pollEvery time.Duration
	commands  chan command
	out       chan<- Snapshot
	logger    *slog.Logger
}

func NewSimulator(pollEvery time.Duration, out chan<- Snapshot, logger *slog.Logger) *Simulator {
	return &Simulator{
		pollEvery: pollEvery,
		commands:  make(chan command, 16),
		out:       out,
		logger:    logger,
	}
}

func (s *Simulator) Backend() Backend { return BackendSimulator }

func (s *Simulator) Submit(verb string, value int) bool {
	if !KnownVerbs[verb] {
		return false
	}
	select {
	case s.commands <- command{verb, value}:
		return true
	default:
		return false
	}
}

func (s *Simulator) Run(ctx context.Context) {
	state := Snapshot{
		Channel:      1,
		AutoChannel:  false,
		PowerAvgW:    100,
		PowerPeakW:   140,
		PeakHoldW:    140,
		SWR:          1.05,
		Range:        "100W",
		PeakMode:     "peak_hold",
		PowerMode:    "net",
		AlarmEnabled: true,
		AlarmPowerW:  1500,
		AlarmSWR:     2.0,
		AlarmTripped: false,
		Callsign:     "SIM",
		Coupler:      "LPC501",
		TopMode:      "power_swr",
		FirmwareRev:  "sim-v1",
		Valid:        true,
	}

	tick := time.NewTicker(s.pollEvery)
	defer tick.Stop()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	t0 := time.Now()
	s.logger.Info("simulator backend running", "poll_ms", s.pollEvery.Milliseconds())

	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-s.commands:
			applyCommand(&state, cmd)
			s.logger.Debug("simulator applied command", "verb", cmd.verb, "value", cmd.value)
		case <-tick.C:
			elapsed := time.Since(t0).Seconds()

			// Smooth power oscillation with two superimposed sine
			// waves, plus a touch of noise for realism.
			base := 100.0 + 40*math.Sin(elapsed/2.0) + 8*math.Sin(elapsed*0.91)
			noise := (rng.Float64() - 0.5) * 4
			state.PowerAvgW = math.Max(0, base+noise)
			state.PowerPeakW = state.PowerAvgW * 1.4
			if state.PowerPeakW > state.PeakHoldW {
				state.PeakHoldW = state.PowerPeakW
			}

			state.SWR = 1.0 + math.Abs(0.15*math.Sin(elapsed/3.5)) + (rng.Float64()-0.5)*0.02
			state.AlarmTripped = state.AlarmEnabled && (state.SWR > state.AlarmSWR || state.PowerAvgW > state.AlarmPowerW)

			snap := state
			snap.Timestamp = time.Now().UTC()
			select {
			case s.out <- snap:
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}

func applyCommand(state *Snapshot, cmd command) {
	switch cmd.verb {
	case "mode_step":
		i := indexOfTopMode(state.TopMode)
		state.TopMode = topModeNames[(i+1)%len(topModeNames)]
	case "channel_step":
		if cmd.value == 0 {
			state.AutoChannel = true
			state.Channel = 1
			return
		}
		state.AutoChannel = false
		state.Channel = cmd.value
	case "range_step":
		if cmd.value < 0 || cmd.value >= len(rangeNames) {
			i := indexOfRange(state.Range)
			state.Range = rangeNames[(i+1)%len(rangeNames)]
			return
		}
		state.Range = rangeNames[cmd.value]
	case "alarm_toggle":
		state.AlarmEnabled = !state.AlarmEnabled
	case "peak_toggle":
		if cmd.value < 0 || cmd.value >= len(peakModeNames) {
			i := indexOfPeak(state.PeakMode)
			state.PeakMode = peakModeNames[(i+1)%len(peakModeNames)]
			state.PeakHoldW = 0
			return
		}
		state.PeakMode = peakModeNames[cmd.value]
		state.PeakHoldW = 0
	case "setup_enter":
		state.TopMode = "setup"
	case "setup_exit":
		state.TopMode = "power_swr"
	case "power_mode":
		if cmd.value < 0 || cmd.value >= len(powerModeNames) {
			return
		}
		state.PowerMode = powerModeNames[cmd.value]
	}
}

func indexOfTopMode(name string) int { return indexInSlice(topModeNames, name) }
func indexOfRange(name string) int   { return indexInSlice(rangeNames, name) }
func indexOfPeak(name string) int    { return indexInSlice(peakModeNames, name) }

func indexInSlice(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return 0
}
