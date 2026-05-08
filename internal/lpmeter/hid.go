package lpmeter

// hidDeviceInfo describes a HID device the server can open. Returned by
// the platform-specific enumerator (`enumerateHID`).
type hidDeviceInfo struct {
	Path         string // e.g. "/dev/hidraw0"
	VendorID     uint16
	ProductID    uint16
	Product      string
	Manufacturer string
}

// hidDevice is the minimal interface the HID owner needs from a HID
// device handle. Both the Linux hidraw implementation and the
// stub-on-non-Linux path satisfy it.
type hidDevice interface {
	Read(buf []byte) (int, error)
	Write(buf []byte) (int, error)
	Close() error
	Info() hidDeviceInfo
}
