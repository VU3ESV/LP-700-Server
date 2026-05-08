//go:build !linux

package lpmeter

import "errors"

// On non-Linux platforms the HID backend is unavailable — the binary
// still builds and runs (the simulator backend works everywhere) but
// any attempt to enumerate or open a real device returns an error.
//
// The Linux build uses /dev/hidraw* directly via plain file I/O. macOS
// (IOHIDDevice) and Windows (HID setupapi/HidD_*) would each need a
// non-trivial native binding. Until someone needs to run the server on
// a Mac/Windows shack PC and wire up to a real meter from there, the
// portability question is academic — this server's primary deployment
// is a Pi at the radio.

func enumerateHID() ([]hidDeviceInfo, error) {
	return nil, nil
}

func openHID(_ hidDeviceInfo) (hidDevice, error) {
	return nil, errors.New("HID backend is Linux-only in this build (use -backend simulator on macOS / Windows)")
}
