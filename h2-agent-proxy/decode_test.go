package h2agentproxy

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	agentv1 "cursor/gen/agentv1"

	"google.golang.org/protobuf/proto"
)

func TestDecodeConnectFramesRoundTrip(t *testing.T) {
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{
			ClientHeartbeat: &agentv1.ClientHeartbeat{},
		},
	}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var gzipBuf bytes.Buffer
	writer := gzip.NewWriter(&gzipBuf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	payload := append(encodeConnectFrame(0x00, raw), encodeConnectFrame(0x01, gzipBuf.Bytes())...)
	frames, err := decodeConnectFrames(payload, "gzip", &agentv1.AgentClientMessage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames=%d want 2", len(frames))
	}
	if frames[0].Kind != "client_heartbeat" || frames[0].Error != "" {
		t.Fatalf("frame0=%+v", frames[0])
	}
	if !frames[1].Compressed || frames[1].Kind != "client_heartbeat" || frames[1].Error != "" {
		t.Fatalf("frame1=%+v", frames[1])
	}
}

func TestDecodeCaptureDirectoryWritesJSONL(t *testing.T) {
	message := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{},
		},
	}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01_request.headers.json"), []byte(`{"path":"/agent.v1.AgentService/Run","headers":{"Connect-Content-Encoding":"gzip"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{
			ClientHeartbeat: &agentv1.ClientHeartbeat{},
		},
	}
	clientRaw, err := proto.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01_request.body.bin"), encodeConnectFrame(0, clientRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01_response.body.bin"), encodeConnectFrame(0, raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DecodeCaptureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "01_request.frames.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "01_response.frames.jsonl")); err != nil {
		t.Fatal(err)
	}
	summary, err := os.ReadFile(filepath.Join(dir, "01_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(summary, []byte("client_heartbeat")) || !bytes.Contains(summary, []byte("interaction_update")) {
		t.Fatalf("summary missing kinds: %s", summary)
	}
}
