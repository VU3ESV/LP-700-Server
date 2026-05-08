//go:build linux

package lpmeter

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// enumerateHID walks /sys/class/hidraw and returns one hidDeviceInfo per
// hidrawN node. Pure Go; no libudev / hidapi dependency.
//
// The interesting metadata lives in
// /sys/class/hidraw/hidrawN/device/uevent which contains lines like:
//
//	HID_ID=0003:000004D8:00000001
//	HID_NAME=Microchip Technology Inc. LP-500
//	HID_PHYS=usb-0000:00:14.0-1/input0
//
// Some kernels don't surface manufacturer/product strings via the HID
// uevent; for those we fall back to the parent USB device's
// `manufacturer` and `product` sysfs attributes when available.
func enumerateHID() ([]hidDeviceInfo, error) {
	const sysClass = "/sys/class/hidraw"
	entries, err := os.ReadDir(sysClass)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", sysClass, err)
	}
	out := make([]hidDeviceInfo, 0, len(entries))
	for _, e := range entries {
		name := e.Name() // e.g. "hidraw0"
		dev := "/dev/" + name
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		info := hidDeviceInfo{Path: dev}

		// /sys/class/hidraw/hidrawN is a symlink whose `device`
		// subdirectory is the HID device. The HID device's
		// `device` parent is the USB interface; one level up is
		// the USB device with manufacturer/product sysfs files.
		hidDir := filepath.Join(sysClass, name, "device")

		if data, err := os.ReadFile(filepath.Join(hidDir, "uevent")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				switch {
				case strings.HasPrefix(line, "HID_ID="):
					parts := strings.Split(strings.TrimPrefix(line, "HID_ID="), ":")
					if len(parts) == 3 {
						if v, err := strconv.ParseUint(parts[1], 16, 32); err == nil {
							info.VendorID = uint16(v)
						}
						if p, err := strconv.ParseUint(parts[2], 16, 32); err == nil {
							info.ProductID = uint16(p)
						}
					}
				case strings.HasPrefix(line, "HID_NAME="):
					info.Product = strings.TrimSpace(strings.TrimPrefix(line, "HID_NAME="))
				}
			}
		}

		// Walk up to the USB device for the cleaner Manufacturer
		// / Product strings if the HID uevent didn't carry them.
		if real, err := filepath.EvalSymlinks(hidDir); err == nil {
			usbDev := findUSBDeviceDir(real)
			if usbDev != "" {
				if info.Manufacturer == "" {
					info.Manufacturer = readTrim(filepath.Join(usbDev, "manufacturer"))
				}
				if p := readTrim(filepath.Join(usbDev, "product")); p != "" {
					// Prefer the cleaner USB Product string
					// over the longer HID_NAME (which often
					// includes manufacturer + product).
					info.Product = p
				}
			}
		}

		out = append(out, info)
	}

	// Stable order is nice for `probe -list` output.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// findUSBDeviceDir walks up `real` until it finds a directory with both
// idVendor and idProduct files, which is the USB device node.
func findUSBDeviceDir(start string) string {
	d := start
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(d, "idVendor")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "idProduct")); err == nil {
				return d
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
	return ""
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// linuxHIDRaw is a thin wrapper over an open /dev/hidraw* file.
type linuxHIDRaw struct {
	f    *os.File
	info hidDeviceInfo
}

func (d *linuxHIDRaw) Read(buf []byte) (int, error)  { return d.f.Read(buf) }
func (d *linuxHIDRaw) Write(buf []byte) (int, error) { return d.f.Write(buf) }
func (d *linuxHIDRaw) Close() error                  { return d.f.Close() }
func (d *linuxHIDRaw) Info() hidDeviceInfo           { return d.info }

// openHID opens a single hidraw device. The path can come from
// enumerateHID (e.g. "/dev/hidraw0").
func openHID(info hidDeviceInfo) (hidDevice, error) {
	if info.Path == "" {
		return nil, errors.New("hidDeviceInfo.Path is empty")
	}
	f, err := os.OpenFile(info.Path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (try running with adequate permissions / install the udev rule)", info.Path, err)
	}
	return &linuxHIDRaw{f: f, info: info}, nil
}
