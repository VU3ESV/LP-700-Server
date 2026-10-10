package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestUpdateCheckDefaultsOnAndCanBeTurnedOff(t *testing.T) {
	cfg, err := Load("")
	if err != nil || !cfg.Updates.Check {
		t.Fatalf("defaults: updates.check = %v, err = %v; want true", cfg.Updates.Check, err)
	}

	dir := t.TempDir()
	noSection := filepath.Join(dir, "no-section.toml")
	if err := os.WriteFile(noSection, []byte("[ui]\ntitle = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(noSection); err != nil || !cfg.Updates.Check {
		t.Fatalf("config without [updates]: check = %v, err = %v; want true", cfg.Updates.Check, err)
	}

	off := filepath.Join(dir, "off.toml")
	if err := os.WriteFile(off, []byte("[updates]\ncheck = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(off); err != nil || cfg.Updates.Check {
		t.Fatalf("[updates] check = false: got %v, err = %v", cfg.Updates.Check, err)
	}
}

// The shipped example must exist, parse, name only keys Config knows,
// and leave the update check on. (Load() would quietly return defaults
// for a missing file, which proves nothing.)
func TestExampleConfigLoads(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaults()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		t.Fatalf("config.example.toml has keys Config does not know: %v", unknown)
	}
	if !md.IsDefined("updates", "check") || !cfg.Updates.Check {
		t.Fatalf("config.example.toml should set [updates] check = true, got %v", cfg.Updates.Check)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
}
