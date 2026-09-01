package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leookun/cursor-byok/tab-server/src/config"
	"github.com/leookun/cursor-byok/tab-server/src/proto"
	"github.com/leookun/cursor-byok/tab-server/src/tab"
)

// buildStreamCppRequest encodes a completion request with the cursor in the
// middle of a Go file.
func buildStreamCppRequest(t *testing.T) []byte {
	t.Helper()
	cursor := proto.CursorPosition{Line: 3, Column: 0}
	currentFile, err := encodeCurrentFileInfoForTest(&proto.CurrentFileInfo{
		RelativeWorkspacePath: "math.go",
		Contents:              "package main\n\nfunc add(a int, b int) int {\n",
		LanguageID:            "go",
		CursorPosition:        &cursor,
		TotalNumberOfLines:    4,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := encodeStreamCppRequestForTest(currentFile)
	if err != nil {
		t.Fatal(err)
	}
	return proto.EncodeUnary(request)
}

func TestStreamCppEndToEnd(t *testing.T) {
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Skipf("config unavailable: %v", err)
	}
	tabServer := New(cfg)
	_ = tab.Handler{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/StreamCpp",
		bytesReader(buildStreamCppRequest(t)))
	request.Header.Set("Content-Type", "application/proto")
	tabServer.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", recorder.Code, recorder.Body.String())
	}
	frames, err := proto.DecodeEnvelope(recorder.Body.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var text string
	for _, frame := range frames {
		if frame.IsEndStream() {
			continue
		}
		response := decodeStreamCppResponseForTest(t, frame.Payload)
		text += response.Text
	}
	t.Logf("completion: %q", text)
	if text == "" {
		t.Fatal("expected non-empty completion")
	}
}
