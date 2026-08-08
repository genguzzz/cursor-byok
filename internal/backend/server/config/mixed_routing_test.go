package config

import "testing"

func TestMixedModelRoutingDefaultsEnabled(t *testing.T) {
	cfg, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !cfg.Features.MixedModelRouting.IsEnabled() {
		t.Fatal("expected mixed model routing enabled by default")
	}
}

func TestMixedModelRoutingExplicitDisable(t *testing.T) {
	disabled := false
	cfg, err := NormalizeConfig(Config{Features: FeaturesConfig{MixedModelRouting: MixedModelRoutingConfig{Enabled: &disabled}}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Features.MixedModelRouting.IsEnabled() {
		t.Fatal("expected mixed model routing disabled")
	}
}
