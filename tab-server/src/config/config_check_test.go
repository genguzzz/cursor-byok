package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRealConfig(t *testing.T) {
	path := filepath.Join("..", "..", "config.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skip("config.yaml not present")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if cfg.Upstream.Model == "" {
		t.Fatal("model empty")
	}
	if cfg.Token == "" {
		t.Fatal("token empty")
	}
	t.Logf("model=%s base=%s endpoint=%s listen=%s", cfg.Upstream.Model, cfg.Upstream.BaseURL, cfg.Upstream.Endpoint, cfg.Server.ListenAddr)
}
