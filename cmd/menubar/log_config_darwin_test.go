//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteDebugLogEnabledPreservesOtherFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := "log: false\nproxyListenAddr: 127.0.0.1:18080\nmodelAdapters: []\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	enabled, err := readDebugLogEnabledFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("expected log disabled by default")
	}

	if err := writeDebugLogEnabledToFile(path, true); err != nil {
		t.Fatal(err)
	}
	enabled, err = readDebugLogEnabledFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("expected log enabled after write")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"log: true", "proxyListenAddr: 127.0.0.1:18080", "modelAdapters:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}

	if err := writeDebugLogEnabledToFile(path, false); err != nil {
		t.Fatal(err)
	}
	enabled, err = readDebugLogEnabledFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("expected log disabled after second write")
	}
}

func TestReadDebugLogEnabledMissingFile(t *testing.T) {
	t.Parallel()
	enabled, err := readDebugLogEnabledFromFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("missing file should mean disabled")
	}
}

func TestDebugSyncActionFrom(t *testing.T) {
	t.Parallel()
	if got := debugSyncActionFrom(true, false); got != debugSyncStart {
		t.Fatalf("log on / debug off -> start, got %d", got)
	}
	if got := debugSyncActionFrom(false, true); got != debugSyncStop {
		t.Fatalf("log off / debug on -> stop, got %d", got)
	}
	if got := debugSyncActionFrom(true, true); got != debugSyncNone {
		t.Fatalf("both on -> none, got %d", got)
	}
	if got := debugSyncActionFrom(false, false); got != debugSyncNone {
		t.Fatalf("both off -> none, got %d", got)
	}
}

func TestStopDebugLockedRestartKeepsLogConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("log: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	debugLogConfigFileOverride = path
	t.Cleanup(func() { debugLogConfigFileOverride = "" })

	resetDebugState()
	debugMu.Lock()
	defer debugMu.Unlock()
	debugState.enabled = true
	stopDebugLocked(false)
	enabled, err := readDebugLogEnabledFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("restart stop should keep log: true")
	}
}

func TestStopDebugLockedPersistClearsLogConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("log: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	debugLogConfigFileOverride = path
	t.Cleanup(func() { debugLogConfigFileOverride = "" })

	resetDebugState()
	debugMu.Lock()
	defer debugMu.Unlock()
	debugState.enabled = true
	stopDebugLocked(true)
	enabled, err := readDebugLogEnabledFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("real stop should persist log: false")
	}
}
