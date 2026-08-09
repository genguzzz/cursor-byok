package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeStoreTestConfig(t *testing.T, path string, adapters []ModelAdapterConfig) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ModelAdapters = adapters
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoadMissingFileInitializesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := NewStore(path, filepath.Join(dir, "logs"))
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelAdapters) != 0 {
		t.Fatalf("expected empty adapters, got %d", len(cfg.ModelAdapters))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoadEmptyFileDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path, filepath.Join(dir, "logs"))
	_, err := store.Load(context.Background())
	if !errors.Is(err, ErrEmptyConfigFile) {
		t.Fatalf("err=%v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "" {
		t.Fatalf("empty file was rewritten: %q", got)
	}
}

func TestStoreLoadEmptyFileRestoresLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := NewStore(path, filepath.Join(dir, "logs"))
	writeStoreTestConfig(t, path, []ModelAdapterConfig{testModelAdapter("keep-me", 1)})
	if _, err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), mustNormalizeTestConfig(t, []ModelAdapterConfig{testModelAdapter("keep-me", 1)})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelAdapters) != 1 || cfg.ModelAdapters[0].DisplayName != "keep-me" {
		t.Fatalf("restored %+v", cfg.ModelAdapters)
	}
}

func TestStoreSaveRefusesEmptyOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := NewStore(path, filepath.Join(dir, "logs"))
	original := mustNormalizeTestConfig(t, []ModelAdapterConfig{
		testModelAdapter("one", 1),
		testModelAdapter("two", 2),
	})
	if _, err := store.Save(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(context.Background(), DefaultConfig())
	if !errors.Is(err, ErrDestructiveConfigWrite) {
		t.Fatalf("err=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("destructive save mutated config.yaml")
	}
}

func TestStoreSaveAllowsNonEmptyUpdateAndWritesLastGood(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	store := NewStore(path, filepath.Join(dir, "logs"))
	first := mustNormalizeTestConfig(t, []ModelAdapterConfig{testModelAdapter("one", 1)})
	if _, err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.LastAgentModelHash = "abc123"
	second.ModelAdapters = append([]ModelAdapterConfig{}, first.ModelAdapters...)
	second.ModelAdapters = append(second.ModelAdapters, testModelAdapter("two", 2))
	if _, err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastAgentModelHash != "abc123" || len(loaded.ModelAdapters) != 2 {
		t.Fatalf("loaded hash=%q adapters=%d", loaded.LastAgentModelHash, len(loaded.ModelAdapters))
	}
	backup, err := os.ReadFile(path + lastGoodBackupSuffix)
	if err != nil {
		t.Fatal(err)
	}
	count, ok := probeModelAdapterCount(backup)
	if !ok || count < 1 {
		t.Fatalf("last-good adapters count=%d ok=%v", count, ok)
	}
}

func TestStoreLoadPersistsMissingListenAddrsWithoutDroppingAdapters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("" +
		"log: true\n" +
		"modelAdapters:\n" +
		"  - displayName: keep-me\n" +
		"    type: openai\n" +
		"    baseURL: https://api.example.com/v1\n" +
		"    apiKey: test-key\n" +
		"    tooltipData: keep-me\n" +
		"    modelID: keep-me\n" +
		"    reasoningEffort: medium\n" +
		"    openAIEndpoint: /v1/responses\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path, filepath.Join(dir, "logs"))
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelAdapters) != 1 || cfg.ModelAdapters[0].DisplayName != "keep-me" {
		t.Fatalf("adapters=%+v", cfg.ModelAdapters)
	}
	if cfg.BackendListenAddr != DefaultBackendListenAddr {
		t.Fatalf("backendListenAddr=%q", cfg.BackendListenAddr)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count, ok := probeModelAdapterCount(onDisk)
	if !ok || count != 1 {
		t.Fatalf("persisted adapters count=%d ok=%v", count, ok)
	}
}

func TestStoreLoadDoesNotPersistDefaultOverTruncatedAdapterYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte("log: false\nmodelAdapters: {broken: true}\ndisplayName: keep-me\nmodelID: keep-me\nbaseURL: https://x\napiKey: k\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path, filepath.Join(dir, "logs"))
	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("expected load error for malformed adapters")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("malformed file was rewritten:\n%s", got)
	}
}

func mustNormalizeTestConfig(t *testing.T, adapters []ModelAdapterConfig) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ModelAdapters = adapters
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}
