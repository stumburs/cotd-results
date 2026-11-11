package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	raw := `
[discord]
webhook_url = "https://discord.com/api/webhooks/123/token"

[nadeo]
login = "login"
password = "password"

[clubs]
ids = [42]
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Settings.PollIntervalSeconds != 60 {
		t.Errorf("poll interval = %d, want 60", cfg.Settings.PollIntervalSeconds)
	}
	if len(cfg.Cups.Partitions) != 1 || cfg.Cups.Partitions[0] != "crossplay" {
		t.Errorf("partitions = %v, want [crossplay]", cfg.Cups.Partitions)
	}
	if len(cfg.Cups.Editions) != 3 {
		t.Errorf("editions = %v, want [1 2 3]", cfg.Cups.Editions)
	}
	if cfg.editionLabel(2) != "Cup of the Night" {
		t.Errorf("edition label = %q", cfg.editionLabel(2))
	}
	if cfg.Discord.Username != "COTD Results" {
		t.Errorf("username = %q", cfg.Discord.Username)
	}
}

func TestLoadConfigRejectsMissingRequiredFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[discord]\nwebhook_url = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestExampleConfigParses(t *testing.T) {
	cfg, err := LoadConfig("config.example.toml")
	if err != nil {
		t.Fatalf("config.example.toml is invalid: %v", err)
	}
	if cfg.editionLabel(1) != "Cup of the Day" || cfg.editionLabel(3) != "Cup of the Morning" {
		t.Errorf("unexpected edition labels: %v", cfg.Cups.EditionLabels)
	}
}

func TestCupFilters(t *testing.T) {
	cfg := defaultConfig()
	if !cfg.wantsPartition("crossplay") {
		t.Error("crossplay should be wanted")
	}
	if cfg.wantsPartition("luna") {
		t.Error("luna should be filtered out by default")
	}
	if !cfg.wantsEdition(2) {
		t.Error("edition 2 should be wanted")
	}
	if cfg.wantsEdition(4) {
		t.Error("edition 4 should not be wanted")
	}
}
