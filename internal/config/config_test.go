package config

import (
	"os"
	"path/filepath"
	"testing"
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

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "deploy", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Updates.Check {
		t.Fatal("deploy/config.example.toml should leave the update check on")
	}
}
