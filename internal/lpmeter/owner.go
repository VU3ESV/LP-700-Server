package lpmeter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Backend tags the source of telemetry (helpful for the web client to
// surface "you are looking at simulated data").
type Backend string

const (
	BackendHID       Backend = "hid"
	BackendSimulator Backend = "simulator"
)

// Source is anything that produces snapshots and accepts named control
// verbs. Both the real HID backend and the simulator implement it; the
// hub doesn't care which is wired up.
type Source interface {
	// Run blocks until ctx is cancelled. Errors during the loop are
	// logged; the implementation is expected to self-recover (HID
	// reconnect on disconnect; simulator never errors).
	Run(ctx context.Context)
	// Submit queues a control verb for the next interleave slot.
	// Returns false if the queue is full or the verb is unknown so
	// callers can NACK.
	Submit(verb string, value int) bool
	// Backend identifies which implementation is running.
	Backend() Backend
}

// HIDOwner is the single goroutine that holds the LP-500/700 HID handle
// and arbitrates reads (poll responses) and writes (control commands)
// so they never collide.
type HIDOwner struct {
	vendorID  uint16
	productID uint16
	pollEvery time.Duration
	commands  chan command
	out       chan<- Snapshot
	logger    *slog.Logger
}

type command struct {
	verb  string
	value int
}

// NewHIDOwner builds an owner that will open an LP-500/700 by VID/PID
// (when both are non-zero) or by Product-string match ("LP-500" /
// "LP-700") otherwise. Writes snapshots to `out`.
func NewHIDOwner(vid, pid uint16, pollEvery time.Duration, out chan<- Snapshot, logger *slog.Logger) *HIDOwner {
	return &HIDOwner{
		vendorID:  vid,
		productID: pid,
		pollEvery: pollEvery,
		commands:  make(chan command, 16),
		out:       out,
		logger:    logger,
	}
}

func (o *HIDOwner) Backend() Backend { return BackendHID }

func (o *HIDOwner) Submit(verb string, value int) bool {
	if !KnownVerbs[verb] {
		return false
	}
	select {
	case o.commands <- command{verb, value}:
		return true
	default:
		return false
	}
}

// Run loops forever, opening the device and reading IN reports, reopening
// on error with exponential backoff. Returns only when ctx is cancelled.
func (o *HIDOwner) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		err := o.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			o.logger.Warn("hid loop ended", "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (o *HIDOwner) runOnce(ctx context.Context) error {
	info, err := o.findDevice()
	if err != nil {
		return fmt.Errorf("find device: %w", err)
	}
	dev, err := openHID(info)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer dev.Close()
	o.logger.Info("hid open",
		"path", info.Path,
		"vendor_id", fmt.Sprintf("0x%04x", info.VendorID),
		"product_id", fmt.Sprintf("0x%04x", info.ProductID),
		"product", info.Product,
		"manufacturer", info.Manufacturer)

	// Reads block in a goroutine so the main loop can still drain
	// commands even when the device is silent. The read goroutine
	// signals the main loop to bail by sending its error.
	frames := make(chan []byte, 4)
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, ReportSize)
		for {
			n, err := dev.Read(buf)
			if err != nil {
				readErr <- err
				return
			}
			if n != ReportSize {
				// hidraw on Linux can return shorter reads
				// when the device sends a smaller report than
				// our buffer (very unusual for vendor HIDs).
				// Skip rather than mis-parse.
				continue
			}
			frame := make([]byte, ReportSize)
			copy(frame, buf)
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()

	// pollTicker drives both the active poll ('0' command) and the
	// drain of any queued control verbs from clients. The Node-RED
	// reference flow polls explicitly to get fresh telemetry; matching
	// that here.
	pollTicker := time.NewTicker(o.pollEvery)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return fmt.Errorf("hid read: %w", err)
		case frame := <-frames:
			snap, err := Decode(frame)
			if err != nil {
				if IsSkippable(err) {
					continue
				}
				o.logger.Debug("decode error", "err", err, "raw", fmt.Sprintf("%x", frame[:minInt(16, len(frame))]))
				continue
			}
			o.logger.Debug("frame",
				"channel", snap.Channel,
				"power_avg_w", snap.PowerAvgW,
				"power_peak_w", snap.PowerPeakW,
				"swr", snap.SWR,
				"range", snap.Range)
			select {
			case o.out <- snap:
			case <-ctx.Done():
				return nil
			default:
				// Hub is slow; drop this sample. Next IN report will replace it.
			}
		case <-pollTicker.C:
			if err := o.drainCommands(dev); err != nil {
				return err
			}
			if err := writeReport(dev, PollReport()); err != nil {
				return fmt.Errorf("poll: %w", err)
			}
		}
	}
}

// drainCommands writes any queued control verbs as 64-byte OUT reports
// with a short post-write settle so the meter can fully process each
// before the next.
func (o *HIDOwner) drainCommands(dev hidDevice) error {
	const postWriteSettle = 50 * time.Millisecond
	for {
		select {
		case cmd := <-o.commands:
			out, err := EncodeCommand(cmd.verb, cmd.value)
			if err != nil {
				o.logger.Warn("encode command", "verb", cmd.verb, "err", err)
				continue
			}
			if err := writeReport(dev, out); err != nil {
				return fmt.Errorf("hid write %s: %w", cmd.verb, err)
			}
			o.logger.Debug("sent command", "verb", cmd.verb, "value", cmd.value)
			time.Sleep(postWriteSettle)
		default:
			return nil
		}
	}
}

// writeReport sends a 64-byte HID OUT report. The Linux hidraw driver
// expects the first byte of the buffer to be the Report ID — the
// LP-500/700's mikroE HID stack uses no Report IDs, so we prepend a
// 0x00 to make a 65-byte buffer where bytes [1..] are the 64-byte
// payload.
func writeReport(dev hidDevice, payload []byte) error {
	buf := make([]byte, 1+len(payload))
	copy(buf[1:], payload)
	_, err := dev.Write(buf)
	return err
}

func (o *HIDOwner) findDevice() (hidDeviceInfo, error) {
	devices, err := enumerateHID()
	if err != nil {
		return hidDeviceInfo{}, fmt.Errorf("enumerate: %w", err)
	}
	if len(devices) == 0 {
		return hidDeviceInfo{}, errors.New("no HID devices enumerable (try `lp700-server probe -list`)")
	}
	// Match by VID:PID first when both are configured.
	if o.vendorID != 0 && o.productID != 0 {
		for _, d := range devices {
			if d.VendorID == o.vendorID && d.ProductID == o.productID {
				return d, nil
			}
		}
	}
	for _, d := range devices {
		if isLPMeter(d.Product) {
			return d, nil
		}
	}
	return hidDeviceInfo{}, errors.New("no LP-500/LP-700 found in HID enumeration")
}

func isLPMeter(product string) bool {
	p := strings.ToUpper(product)
	return strings.Contains(p, "LP-500") || strings.Contains(p, "LP-700") ||
		strings.Contains(p, "LP500") || strings.Contains(p, "LP700")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
