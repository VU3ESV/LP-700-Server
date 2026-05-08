package lpmeter

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ProbeMode picks one of three diagnostic actions the binary's `probe`
// subcommand can perform.
type ProbeMode string

const (
	ProbeList    ProbeMode = "list"    // enumerate every HID
	ProbeDump    ProbeMode = "dump"    // print every IN report from the matched LP-500/700
	ProbeCapture ProbeMode = "capture" // write a fixture file
)

// ProbeOptions wraps the CLI flags driving the probe subcommand.
type ProbeOptions struct {
	Mode      ProbeMode
	OutPath   string        // for ProbeCapture
	Duration  time.Duration // for ProbeCapture; 0 = until ^C
	VendorID  uint16
	ProductID uint16
}

// RunProbe executes one of the diagnostic modes. Output goes to `w`.
func RunProbe(ctx context.Context, opts ProbeOptions, w io.Writer) error {
	switch opts.Mode {
	case ProbeList:
		return runList(w, opts)
	case ProbeDump:
		return runDump(ctx, w, opts)
	case ProbeCapture:
		return runCapture(ctx, w, opts)
	default:
		return fmt.Errorf("unknown probe mode %q", opts.Mode)
	}
}

func runList(w io.Writer, opts ProbeOptions) error {
	devs, err := enumerateHID()
	if err != nil {
		return fmt.Errorf("enumerate: %w", err)
	}
	if len(devs) == 0 {
		fmt.Fprintln(w, "No HID devices enumerable. On Linux, ensure /dev/hidraw* exists and the lp700 user can read it (install the udev rule).")
		return nil
	}
	fmt.Fprintf(w, "%-15s  %-15s  %-7s  %-7s  %-30s  %s\n", "marker", "path", "vid", "pid", "product", "manufacturer")
	for _, d := range devs {
		marker := "  "
		if isLPMeter(d.Product) || (opts.VendorID != 0 && opts.ProductID != 0 && d.VendorID == opts.VendorID && d.ProductID == opts.ProductID) {
			marker = "* LP-500/700"
		}
		fmt.Fprintf(w, "%-15s  %-15s  0x%04x   0x%04x   %-30s  %s\n",
			marker, d.Path, d.VendorID, d.ProductID, truncate(d.Product, 30), d.Manufacturer)
	}
	return nil
}

func runDump(ctx context.Context, w io.Writer, opts ProbeOptions) error {
	dev, info, err := openLPMeter(opts)
	if err != nil {
		return err
	}
	defer dev.Close()
	fmt.Fprintf(w, "# Opened %s (%04x:%04x %q manuf=%q)\n", info.Path, info.VendorID, info.ProductID, info.Product, info.Manufacturer)
	fmt.Fprintln(w, "# Each line is one IN report. raw=hex  decoded=human")

	// Drive a poll on a ticker so the meter actually emits frames.
	pollErr := make(chan error, 1)
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := writeReport(dev, PollReport()); err != nil {
					pollErr <- err
					return
				}
			}
		}
	}()

	buf := make([]byte, ReportSize)
	for ctx.Err() == nil {
		select {
		case e := <-pollErr:
			return fmt.Errorf("poll: %w", e)
		default:
		}
		n, err := dev.Read(buf)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			continue
		}
		printFrame(w, buf[:n])
	}
	return nil
}

func runCapture(ctx context.Context, w io.Writer, opts ProbeOptions) error {
	if opts.OutPath == "" {
		return errors.New("capture requires -capture <path>")
	}
	dev, info, err := openLPMeter(opts)
	if err != nil {
		return err
	}
	defer dev.Close()

	f, err := os.Create(opts.OutPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", opts.OutPath, err)
	}
	defer f.Close()

	deadline := time.Time{}
	if opts.Duration > 0 {
		deadline = time.Now().Add(opts.Duration)
	}

	header := fmt.Sprintf("# LP-500/700 HID capture\n# device: %s %04x:%04x %q\n# started: %s\n# duration: %s\n# format: 64 bytes per frame, raw\n",
		info.Path, info.VendorID, info.ProductID, info.Product, time.Now().Format(time.RFC3339), opts.Duration)
	if _, err := f.WriteString(header); err != nil {
		return err
	}
	fmt.Fprintf(w, "Capturing to %s. Press ^C to stop.\n", opts.OutPath)

	// Drive a poll ticker as in dump.
	pollErr := make(chan error, 1)
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := writeReport(dev, PollReport()); err != nil {
					pollErr <- err
					return
				}
			}
		}
	}()

	buf := make([]byte, ReportSize)
	frames := 0
	for ctx.Err() == nil {
		select {
		case e := <-pollErr:
			return fmt.Errorf("poll: %w", e)
		default:
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		n, err := dev.Read(buf)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if n != ReportSize {
			continue
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		frames++
	}
	fmt.Fprintf(w, "Wrote %d frames to %s\n", frames, opts.OutPath)
	return nil
}

func openLPMeter(opts ProbeOptions) (hidDevice, hidDeviceInfo, error) {
	devs, err := enumerateHID()
	if err != nil {
		return nil, hidDeviceInfo{}, fmt.Errorf("enumerate: %w", err)
	}
	if len(devs) == 0 {
		return nil, hidDeviceInfo{}, errors.New("no HID devices enumerable")
	}
	for _, d := range devs {
		if (opts.VendorID != 0 && opts.ProductID != 0 && d.VendorID == opts.VendorID && d.ProductID == opts.ProductID) || isLPMeter(d.Product) {
			dev, err := openHID(d)
			if err != nil {
				return nil, hidDeviceInfo{}, fmt.Errorf("open: %w", err)
			}
			return dev, d, nil
		}
	}
	return nil, hidDeviceInfo{}, errors.New("no LP-500/LP-700 found")
}

func printFrame(w io.Writer, frame []byte) {
	human := "skip"
	if snap, err := Decode(frame); err == nil {
		human = fmt.Sprintf("ch=%d auto=%t avg=%.1fW peak=%.1fW swr=%.2f range=%s",
			snap.Channel, snap.AutoChannel, snap.PowerAvgW, snap.PowerPeakW, snap.SWR, snap.Range)
	} else if IsSkippable(err) {
		human = fmt.Sprintf("non-telemetry frame type=0x%02x", frame[0])
	} else {
		human = "decode error: " + err.Error()
	}
	fmt.Fprintf(w, "raw=%s  decoded=%s\n", hex.EncodeToString(frame), human)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// IsLPMeterProductString is exported so the main package can use the same
// match function when reporting backend selection in the config endpoint.
func IsLPMeterProductString(s string) bool { return isLPMeter(s) }

// HasLPMeterAttached returns true when the host has at least one HID
// whose product string matches LP-500 / LP-700, OR matches the default
// Microchip VID:PID. Used by the `auto` backend selection in main.
func HasLPMeterAttached() bool {
	devs, err := enumerateHID()
	if err != nil {
		return false
	}
	for _, d := range devs {
		if isLPMeter(d.Product) {
			return true
		}
		if d.VendorID == DefaultVendorID && d.ProductID == DefaultProductID {
			return true
		}
	}
	return false
}
