package proto

import (
	"testing"
)

func TestConnectEnvelopeRoundTrip(t *testing.T) {
	payload := []byte("hello-proto")
	framed := EncodeUnary(payload)
	frames, err := DecodeEnvelope(framed)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("want 1 frame, got %d", len(frames))
	}
	if string(frames[0].Payload) != string(payload) {
		t.Fatalf("payload mismatch")
	}
	if frames[0].IsEndStream() {
		t.Fatal("unary frame must not be terminal")
	}
}

func TestEndStreamFrame(t *testing.T) {
	frame := EncodeEndStream()
	frames, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !frames[0].IsEndStream() {
		t.Fatal("want terminal frame")
	}
	if string(frames[0].Payload) != "{}" {
		t.Fatalf("want {}, got %s", frames[0].Payload)
	}
}

func TestErrorEndStreamFrame(t *testing.T) {
	frame := EncodeErrorEndStream(Unavailable(errTest))
	frames, err := DecodeEnvelope(frame)
	if err != nil {
		t.Fatal(err)
	}
	if string(frames[0].Payload) == "{}" {
		t.Fatal("error frame must not be empty payload")
	}
	t.Logf("error payload: %s", frames[0].Payload)
}

func TestStreamCppResponseRoundTrip(t *testing.T) {
	startLine := int32(10)
	done := true
	encoded := EncodeStreamCppResponse(&StreamCppResponse{
		Text:                "return a + b",
		SuggestionStartLine: &startLine,
		DoneStream:          &done,
	})
	if len(encoded) == 0 {
		t.Fatal("empty encoding")
	}
	t.Logf("encoded %d bytes: %x", len(encoded), encoded)
}

func TestCppConfigResponseEncoding(t *testing.T) {
	above := int32(80)
	below := int32(80)
	on := true
	ghost := true
	encoded := EncodeCppConfigResponse(&CppConfigResponse{
		AboveRadius:                  &above,
		BelowRadius:                  &below,
		IsOn:                         &on,
		IsGhostText:                  &ghost,
		GlobalDebounceDurationMillis: 150,
		ClientDebounceDurationMillis: 100,
		CppURL:                       "http://127.0.0.1:8041",
		UseWhitespaceDiffHistory:     true,
		AllowsTabChunks:              true,
	})
	t.Logf("config encoded %d bytes: %x", len(encoded), encoded)
}
