package execbridge

import "testing"

func TestIsReadImagePath(t *testing.T) {
	cases := map[string]bool{
		"/a/b.jpg":  true,
		"/a/b.JPEG": true,
		"/a/b.png":  true,
		"/a/b.webp": true,
		"/a/b.gif":  true,
		"/a/b.go":   false,
		"":          false,
	}
	for path, want := range cases {
		if got := isReadImagePath(path); got != want {
			t.Fatalf("isReadImagePath(%q)=%v want %v", path, got, want)
		}
	}
}

func TestIsImageBinaryPayload(t *testing.T) {
	if !isImageBinaryPayload([]byte{0xFF, 0xD8, 0xFF, 0x00}) {
		t.Fatal("jpeg magic should match")
	}
	if !isImageBinaryPayload([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Fatal("png magic should match")
	}
	if isImageBinaryPayload([]byte("package main")) {
		t.Fatal("text payload must not match")
	}
}

func TestReadReplayImageBinaryLimitAboveDefault(t *testing.T) {
	if readReplayImageBinaryLimit <= readReplayBinaryLimit {
		t.Fatalf("image limit %d should exceed binary limit %d", readReplayImageBinaryLimit, readReplayBinaryLimit)
	}
	if readReplayImageBinaryLimit < 266*1024 {
		t.Fatalf("image limit %d too small for CodeBuddy-sized JPEG", readReplayImageBinaryLimit)
	}
}
