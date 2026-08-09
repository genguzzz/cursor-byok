package proxydebugger

import (
	"encoding/binary"
	"net/http"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

	"google.golang.org/protobuf/proto"
)

func TestIngestUpstreamHopDecodesOfficialRunSSEFrames(t *testing.T) {
	server, err := New(Config{
		ProxyAddr: "127.0.0.1:19095",
		UIAddr:    "127.0.0.1:19094",
	})
	if err != nil {
		t.Fatal(err)
	}

	requestFrame := mustConnectFrame(t, 0, mustMarshal(t, &aiserverv1.BidiRequestId{RequestId: "official-rid"}))
	responseMsg := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ConversationCheckpointUpdate{
			ConversationCheckpointUpdate: &agentv1.ConversationStateStructure{
				RootPromptMessagesJson: [][]byte{[]byte(`{"role":"system"}`)},
			},
		},
	}
	responseFrame := mustConnectFrame(t, 0, mustMarshal(t, responseMsg))

	server.IngestUpstreamHop(UpstreamHop{
		StartedAt: time.Now(),
		Method:    http.MethodPost,
		URL:       "https://api2.cursor.sh" + runSSEPath,
		Host:      "api2.cursor.sh",
		Path:      runSSEPath,
		Status:    200,
		RequestHeader: http.Header{
			"Content-Type": []string{"application/connect+proto"},
		},
		ResponseHeader: http.Header{
			"Content-Type":             []string{"text/event-stream"},
			"Connect-Content-Encoding": []string{"gzip"},
		},
		RequestBody:  requestFrame,
		ResponseBody: responseFrame,
	})

	summaries := server.store.summaries()
	if len(summaries) != 1 {
		t.Fatalf("summaries=%d want 1", len(summaries))
	}
	exchange, ok := server.store.get(summaries[0].ID)
	if !ok {
		t.Fatal("missing exchange")
	}
	if exchange.Server != ServerOfficial || exchange.CaptureSource != CaptureSourceUpstream {
		t.Fatalf("server/source = %s/%s", exchange.Server, exchange.CaptureSource)
	}
	if len(exchange.Request.Frames) == 0 {
		t.Fatal("official RunSSE request frames should be decoded offline")
	}
	if exchange.RequestID != "official-rid" {
		t.Fatalf("request id = %q", exchange.RequestID)
	}
	if len(exchange.Response.Frames) == 0 {
		t.Fatal("official RunSSE response frames should be decoded offline")
	}
	if exchange.FrameCount != len(exchange.Response.Frames) {
		t.Fatalf("frameCount=%d frames=%d", exchange.FrameCount, len(exchange.Response.Frames))
	}
	if exchange.ResponseKind != "conversation_checkpoint_update" {
		t.Fatalf("response kind = %q", exchange.ResponseKind)
	}
	if !strings.Contains(exchange.Response.Frames[0].JSON, "conversation_checkpoint_update") &&
		!strings.Contains(exchange.Response.Frames[0].JSON, "root_prompt_messages_json") {
		t.Fatalf("unexpected frame json: %s", exchange.Response.Frames[0].JSON)
	}
}

func TestFinishResponseBodyDoesNotDuplicateMITMFrames(t *testing.T) {
	server := &Server{
		store:  newExchangeStore(defaultMaxStoreBytes, 0),
		config: Config{}.normalized(),
	}
	existing := FrameView{Index: 0, Kind: "interaction_update", JSON: `{"interaction_update":{}}`}
	server.store.create(&Exchange{
		ExchangeSummary: ExchangeSummary{ID: "1", StartedAt: time.Now(), Path: runSSEPath},
		Response:        Payload{Frames: []FrameView{existing}},
	})
	body := mustConnectFrame(t, 0, mustMarshal(t, &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_ConversationCheckpointUpdate{
			ConversationCheckpointUpdate: &agentv1.ConversationStateStructure{},
		},
	}))
	server.finishResponseBody("1", runSSEPath, "gzip", body, int64(len(body)), false, nil)
	exchange, _ := server.store.get("1")
	if len(exchange.Response.Frames) != 1 {
		t.Fatalf("frames=%d want 1 (no duplicate offline decode)", len(exchange.Response.Frames))
	}
	if exchange.Response.Frames[0].Kind != "interaction_update" {
		t.Fatalf("kept frame kind = %q", exchange.Response.Frames[0].Kind)
	}
}

func mustMarshal(t *testing.T, message proto.Message) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func mustConnectFrame(t *testing.T, flags byte, payload []byte) []byte {
	t.Helper()
	frame := make([]byte, 5+len(payload))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}
