package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"lp700-server/internal/lpmeter"
)

type Config struct {
	Meter  Meter  `toml:"meter"`
	Server Server `toml:"server"`
	UI     UI     `toml:"ui"`
}

type Meter struct {
	// Backend selects the telemetry source: "hid" for a real LP-500/700,
	// "simulator" for synthesised frames, or "auto" to prefer hid when a
	// matching device is present and fall back to simulator.
	Backend   string `toml:"backend"`
	VendorID  uint16 `toml:"vendor_id"`
	ProductID uint16 `toml:"product_id"`
	PollMs    int    `toml:"poll_ms"`
}

type Server struct {
	Listen       string `toml:"listen"`
	HeartbeatMs  int    `toml:"heartbeat_ms"`
	MaxClients   int    `toml:"max_clients"`
	AllowControl bool   `toml:"allow_control"`
}

type UI struct {
	Title string `toml:"title"`
}

func defaults() Config {
	return Config{
		Meter: Meter{
			Backend: "auto",
			// Default Microchip VID:PID derived from the KD4Z Node-RED
			// flow (`vid: "1240" pid: "1"`, decimal). 1240 = 0x04D8 is
			// the standard Microchip USB VID for PIC32 devices, and
			// the LP-500/700 firmware ships with product=0x0001.
			VendorID:  lpmeter.DefaultVendorID,
			ProductID: lpmeter.DefaultProductID,
			PollMs:    40,
		},
		Server: Server{
			Listen:       "0.0.0.0:8089",
			HeartbeatMs:  2000,
			MaxClients:   32,
			AllowControl: true,
		},
		UI: UI{
			Title: "LP-500 / LP-700",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := defaults()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	switch c.Meter.Backend {
	case "auto", "hid", "simulator":
	default:
		return fmt.Errorf("meter.backend must be one of: auto, hid, simulator (got %q)", c.Meter.Backend)
	}
	if c.Meter.PollMs < 10 || c.Meter.PollMs > 1000 {
		return fmt.Errorf("meter.poll_ms out of range [10,1000]")
	}
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen must be set")
	}
	if c.Server.MaxClients <= 0 {
		return fmt.Errorf("server.max_clients must be > 0")
	}
	if c.Server.HeartbeatMs < 200 {
		return fmt.Errorf("server.heartbeat_ms must be >= 200")
	}
	return nil
}
