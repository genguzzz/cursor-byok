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
