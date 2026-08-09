package appdata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyLegacyFileSkipsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.yaml")
	target := filepath.Join(dir, "v2.yaml")
	if err := os.WriteFile(source, []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("keep-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	copyLegacyFile(source, target)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep-me\n" {
		t.Fatalf("existing v2 config was overwritten: %q", got)
	}
}

func TestCopyLegacyFileCreatesMissingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.yaml")
	target := filepath.Join(dir, "nested", "v2.yaml")
	if err := os.WriteFile(source, []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	copyLegacyFile(source, target)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "legacy\n" {
		t.Fatalf("got %q", got)
	}
}
