package backend_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/gen/aiserverv1/aiserverv1connect"
	"cursor/internal/backend"
	serverconfig "cursor/internal/backend/server/config"

	"google.golang.org/protobuf/proto"
)

func TestAgentCLIHostListModelsAndAskTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Path, "chat/completions") {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"LOCAL-OK"},"finish_reason":null}]}`,
			`{"id":"cmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(provider.Close)

	cfgPath := filepath.Join(home, ".cursor-local-assistant-v2", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	store := serverconfig.NewStore(cfgPath, filepath.Join(home, ".cursor-local-assistant-v2", "logs"))
	_, err := store.Save(context.Background(), serverconfig.Config{
		BackendListenAddr: "127.0.0.1:18090",
		ProxyListenAddr:   "127.0.0.1:18080",
		ModelAdapters: []serverconfig.ModelAdapterConfig{{
			DisplayName:     "Local Composer",
			Type:            "openai",
			BaseURL:         provider.URL,
			APIKey:          "test-key",
			TooltipData:     "agent cli test adapter",
			ModelID:         "composer-2.5",
			ProviderModelID: "mock-model",
			ReasoningEffort: "medium",
			OpenAIEndpoint:  "/v1/chat/completions",
		}},
	})
	if err != nil {
		t.Fatalf("save config: %v", err)
	}

	host, err := backend.NewHost(store, nil)
	if err != nil {
		t.Fatalf("new host: %v", err)
	}
	handler := host.Handler()
	if handler == nil {
		t.Fatal("host handler is nil")
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	t.Run("healthz", func(t *testing.T) {
		resp, err := server.Client().Get(server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
			t.Fatalf("healthz status=%d body=%q", resp.StatusCode, body)
		}
	})

	t.Run("get_server_config", func(t *testing.T) {
		var got aiserverv1.GetServerConfigResponse
		postProto(t, server, "/aiserver.v1.AiService/GetServerConfig", nil, &got)
		if got.GetHttp2Config() != aiserverv1.Http2Config_HTTP2_CONFIG_FORCE_ALL_DISABLED {
			t.Fatalf("http2 config=%v", got.GetHttp2Config())
		}
		if got.GetIndexingConfig().GetDefaultUserPathEncryptionKey() == "" {
			t.Fatal("missing CLI path encryption key")
		}
	})

	var channelModelID string
	t.Run("list_models", func(t *testing.T) {
		var usable agentv1.GetUsableModelsResponse
		postProto(t, server, "/aiserver.v1.AiService/GetUsableModels", &agentv1.GetUsableModelsRequest{}, &usable)
		if len(usable.GetModels()) != 1 {
			t.Fatalf("usable models=%d", len(usable.GetModels()))
		}
		model := usable.GetModels()[0]
		channelModelID = model.GetModelId()
		if channelModelID == "" || channelModelID == "composer-2.5" {
			t.Fatalf("modelId should be channel hash, got %q", channelModelID)
		}
		if model.GetDisplayName() != "Local Composer" {
			t.Fatalf("displayName=%q", model.GetDisplayName())
		}
		if len(model.GetAliases()) == 0 {
			t.Fatal("expected provider model aliases")
		}

		var defaultModel agentv1.GetDefaultModelForCliResponse
		postProto(t, server, "/aiserver.v1.AiService/GetDefaultModelForCli", &agentv1.GetDefaultModelForCliRequest{}, &defaultModel)
		if defaultModel.GetModel().GetModelId() != channelModelID {
			t.Fatalf("default model=%q want %q", defaultModel.GetModel().GetModelId(), channelModelID)
		}
	})

	t.Run("get_me", func(t *testing.T) {
		var me aiserverv1.GetMeResponse
		postProto(t, server, "/aiserver.v1.DashboardService/GetMe", nil, &me)
		if strings.TrimSpace(me.GetEmail()) == "" && strings.TrimSpace(me.GetAuthId()) == "" {
			t.Fatalf("empty GetMe: %#v", me.ProtoReflect().Descriptor().FullName())
		}
	})

	t.Run("ask_turn", func(t *testing.T) {
		requestID := "cli-req-1"
		conversationID := "cli-conv-1"
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		reqBody, err := proto.Marshal(&aiserverv1.BidiRequestId{RequestId: requestID})
		if err != nil {
			t.Fatal(err)
		}
		streamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/agent.v1.AgentService/RunSSE", bytes.NewReader(encodeConnectFrame(0, reqBody)))
		if err != nil {
			t.Fatal(err)
		}
		streamReq.Header.Set("Content-Type", "application/connect+proto")
		streamReq.Header.Set("Connect-Protocol-Version", "1")

		textCh := make(chan string, 8)
		errCh := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := server.Client().Do(streamReq)
			if err != nil {
				errCh <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				errCh <- fmt.Errorf("RunSSE status=%d body=%s", resp.StatusCode, body)
				return
			}
			text, err := collectRunSSEText(resp.Body)
			if err != nil {
				errCh <- err
				return
			}
			textCh <- text
		}()

		time.Sleep(150 * time.Millisecond)

		if channelModelID == "" {
			t.Fatal("channelModelID unset; list_models must run first")
		}
		clientMsg, err := proto.Marshal(&agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_RunRequest{
				RunRequest: &agentv1.AgentRunRequest{
					ConversationId: proto.String(conversationID),
					RequestedModel: &agentv1.RequestedModel{ModelId: channelModelID},
					Action: &agentv1.ConversationAction{
						Action: &agentv1.ConversationAction_UserMessageAction{
							UserMessageAction: &agentv1.UserMessageAction{
								UserMessage: &agentv1.UserMessage{
									Text:      "Reply with exactly: LOCAL-OK",
									MessageId: "msg-1",
									Mode:      agentv1.AgentMode_AGENT_MODE_ASK,
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		bidi := aiserverv1connect.NewBidiServiceClient(server.Client(), server.URL)
		if _, err := bidi.BidiAppend(ctx, connect.NewRequest(&aiserverv1.BidiAppendRequest{
			RequestId:   &aiserverv1.BidiRequestId{RequestId: requestID},
			Data:        hex.EncodeToString(clientMsg),
			AppendSeqno: 1,
		})); err != nil {
			t.Fatalf("BidiAppend: %v", err)
		}

		select {
		case err := <-errCh:
			t.Fatalf("RunSSE: %v", err)
		case text := <-textCh:
			if !strings.Contains(text, "LOCAL-OK") {
				t.Fatalf("ask turn text=%q", text)
			}
		case <-ctx.Done():
			t.Fatal("ask turn timed out")
		}
		wg.Wait()
	})
}

func postProto(t *testing.T, server *httptest.Server, path string, request proto.Message, response proto.Message) {
	t.Helper()
	var payload []byte
	var err error
	if request != nil {
		payload, err = proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", path, resp.StatusCode, body)
	}
	if err := proto.Unmarshal(body, response); err != nil {
		t.Fatalf("%s unmarshal: %v body=%q", path, err, body)
	}
}

func encodeConnectFrame(flags uint8, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func collectRunSSEText(body io.Reader) (string, error) {
	var text strings.Builder
	for {
		header := make([]byte, 5)
		if _, err := io.ReadFull(body, header); err != nil {
			if err == io.EOF {
				return text.String(), nil
			}
			return text.String(), err
		}
		flags := header[0]
		length := int(binary.BigEndian.Uint32(header[1:5]))
		if length < 0 || length > 16<<20 {
			return text.String(), fmt.Errorf("invalid connect frame length %d", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(body, payload); err != nil {
			return text.String(), err
		}
		if flags&0x02 != 0 {
			return text.String(), nil
		}
		message := &agentv1.AgentServerMessage{}
		if err := proto.Unmarshal(payload, message); err != nil {
			continue
		}
		if update := message.GetInteractionUpdate(); update != nil {
			if delta := update.GetTextDelta(); delta != nil {
				text.WriteString(delta.GetText())
			}
		}
	}
}
