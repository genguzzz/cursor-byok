package modeladapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// capturedRequest 保存 mock server 收到的请求信息。
type capturedRequest struct {
	Headers http.Header
	Body    map[string]any
}

// TestAnthropicTclaudeHeaderBodyAlignment 通过 mock HTTP server 验证 AnthropicAdapter
// 发出的请求头和请求体与 tclaude daemon 期望的格式对齐。
func TestAnthropicTclaudeHeaderBodyAlignment(t *testing.T) {
	var captured capturedRequest

	// mock server 捕获请求并返回最小化的 SSE 响应。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var bodyMap map[string]any
		_ = json.Unmarshal(bodyBytes, &bodyMap)
		captured = capturedRequest{
			Headers: r.Header.Clone(),
			Body:    bodyMap,
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 返回最小化 SSE 流：message_start + message_stop
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"model":"test-model","usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	adapter := NewAnthropicAdapter()
	req := StreamRequest{
		BaseURL:          server.URL + "/v1/messages?beta=true",
		APIKey:           "Bearer placeholder",
		ProviderModelID:  "claude-test-model",
		ModelID:          "claude-test-model",
		MaxTokens:        32000,
		AnthropicMaxTokens: 32000,
		AnthropicThinkingEffort: "high",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var events []ModelEvent
	err := adapter.Stream(ctx, req, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("adapter.Stream returned error: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected at least one event, got none")
	}

	// ========== 验证请求头 ==========
	headers := captured.Headers

	headerChecks := []struct {
		name string
		key  string
		want string
	}{
		{"Accept", "Accept", "application/json"},
		{"User-Agent", "User-Agent", "claude-cli/2.1.154 (external, cli)"},
		{"anthropic-version", "Anthropic-Version", "2023-06-01"},
		{"anthropic-dangerous-direct-browser-access", "Anthropic-Dangerous-Direct-Browser-Access", "true"},
		{"x-app", "X-App", "cli"},
		{"X-Stainless-Lang", "X-Stainless-Lang", "js"},
		{"X-Stainless-OS", "X-Stainless-OS", "MacOS"},
		{"X-Stainless-Package-Version", "X-Stainless-Package-Version", "0.94.0"},
		{"X-Stainless-Retry-Count", "X-Stainless-Retry-Count", "0"},
		{"X-Stainless-Runtime", "X-Stainless-Runtime", "node"},
		{"X-Stainless-Runtime-Version", "X-Stainless-Runtime-Version", "v24.3.0"},
		{"X-Stainless-Timeout", "X-Stainless-Timeout", "600"},
	}
	for _, hc := range headerChecks {
		t.Run("header/"+hc.name, func(t *testing.T) {
			got := headers.Get(hc.key)
			if got != hc.want {
				t.Errorf("header %s = %q, want %q", hc.key, got, hc.want)
			}
		})
	}

	// 验证 X-Stainless-Arch 非空（值取决于运行时架构）
	t.Run("header/X-Stainless-Arch", func(t *testing.T) {
		if arch := headers.Get("X-Stainless-Arch"); arch == "" {
			t.Error("X-Stainless-Arch should not be empty")
		}
	})

	// 验证 X-Claude-Code-Session-Id 是有效 UUID 格式
	t.Run("header/X-Claude-Code-Session-Id", func(t *testing.T) {
		sid := headers.Get("X-Claude-Code-Session-Id")
		if sid == "" {
			t.Fatal("X-Claude-Code-Session-Id should not be empty")
		}
		if len(sid) != 36 {
			t.Errorf("X-Claude-Code-Session-Id should be a UUID (36 chars), got %d chars: %q", len(sid), sid)
		}
	})

	// 验证 anthropic-beta 头包含必需的 beta 特性
	t.Run("header/anthropic-beta", func(t *testing.T) {
		beta := headers.Get("Anthropic-Beta")
		requiredBetas := []string{
			"claude-code-20250219",
			"context-1m-2025-08-07",
			"interleaved-thinking-2025-05-14",
			"context-management-2025-06-27",
			"effort-2025-11-24",
		}
		for _, rb := range requiredBetas {
			if !strings.Contains(beta, rb) {
				t.Errorf("anthropic-beta missing required feature %q, got: %q", rb, beta)
			}
		}
	})

	// 验证 Authorization 头
	t.Run("header/Authorization", func(t *testing.T) {
		auth := headers.Get("Authorization")
		if auth != "Bearer placeholder" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer placeholder")
		}
	})

	// 验证 x-api-key 头不存在（tclaude daemon 仅使用 Authorization: Bearer）
	t.Run("header/no-x-api-key", func(t *testing.T) {
		if key := headers.Get("X-Api-Key"); key != "" {
			t.Errorf("X-Api-Key should not be set for tclaude compatibility, got %q", key)
		}
	})

	// ========== 验证请求体 ==========

	// 验证 thinking 字段：应为 {"type": "adaptive"}，不含 display
	t.Run("body/thinking", func(t *testing.T) {
		thinking, ok := captured.Body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("thinking should be a map, got %T: %v", captured.Body["thinking"], captured.Body["thinking"])
		}
		if thinking["type"] != "adaptive" {
			t.Errorf("thinking.type = %v, want %q", thinking["type"], "adaptive")
		}
		if _, hasDisplay := thinking["display"]; hasDisplay {
			t.Errorf("thinking should not contain 'display' field, got: %v", thinking["display"])
		}
	})

	// 验证 output_config 字段：应为 {"effort": "high"}
	t.Run("body/output_config", func(t *testing.T) {
		oc, ok := captured.Body["output_config"].(map[string]any)
		if !ok {
			t.Fatalf("output_config should be a map, got %T: %v", captured.Body["output_config"], captured.Body["output_config"])
		}
		if oc["effort"] != "high" {
			t.Errorf("output_config.effort = %v, want %q", oc["effort"], "high")
		}
	})

	// 验证 context_management 字段
	t.Run("body/context_management", func(t *testing.T) {
		cm, ok := captured.Body["context_management"].(map[string]any)
		if !ok {
			t.Fatalf("context_management should be a map, got %T: %v", captured.Body["context_management"], captured.Body["context_management"])
		}
		edits, ok := cm["edits"].([]any)
		if !ok || len(edits) != 1 {
			t.Fatalf("context_management.edits should be a slice with 1 element, got %T: %v", cm["edits"], cm["edits"])
		}
		edit, ok := edits[0].(map[string]any)
		if !ok {
			t.Fatalf("context_management.edits[0] should be a map, got %T", edits[0])
		}
		if edit["type"] != "clear_thinking_20251015" {
			t.Errorf("context_management.edits[0].type = %v, want %q", edit["type"], "clear_thinking_20251015")
		}
		if edit["keep"] != "all" {
			t.Errorf("context_management.edits[0].keep = %v, want %q", edit["keep"], "all")
		}
	})

	// 验证 metadata.user_id 字段：应为 JSON 字符串
	t.Run("body/metadata.user_id", func(t *testing.T) {
		meta, ok := captured.Body["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("metadata should be a map, got %T: %v", captured.Body["metadata"], captured.Body["metadata"])
		}
		userID, ok := meta["user_id"].(string)
		if !ok {
			t.Fatalf("metadata.user_id should be a string, got %T: %v", meta["user_id"], meta["user_id"])
		}
		// user_id 是一个 JSON 字符串，解析后应包含 device_id、account_uuid、session_id
		var parsed map[string]any
		if err := json.Unmarshal([]byte(userID), &parsed); err != nil {
			t.Fatalf("metadata.user_id is not valid JSON: %v, raw: %q", err, userID)
		}
		if _, ok := parsed["device_id"].(string); !ok || parsed["device_id"] == "" {
			t.Errorf("metadata.user_id.device_id should be a non-empty string, got: %v", parsed["device_id"])
		}
		if parsed["account_uuid"] != "" {
			t.Errorf("metadata.user_id.account_uuid should be empty string, got: %v", parsed["account_uuid"])
		}
		sessionID, ok := parsed["session_id"].(string)
		if !ok || sessionID == "" {
			t.Errorf("metadata.user_id.session_id should be a non-empty string, got: %v", parsed["session_id"])
		}
		// session_id 应与 X-Claude-Code-Session-Id 头一致
		headerSID := captured.Headers.Get("X-Claude-Code-Session-Id")
		if sessionID != headerSID {
			t.Errorf("metadata.user_id.session_id (%q) should match X-Claude-Code-Session-Id header (%q)", sessionID, headerSID)
		}
	})

	// 验证 stream 字段
	t.Run("body/stream", func(t *testing.T) {
		stream, ok := captured.Body["stream"].(bool)
		if !ok {
			t.Fatalf("stream should be a bool, got %T: %v", captured.Body["stream"], captured.Body["stream"])
		}
		if !stream {
			t.Error("stream should be true")
		}
	})

	// 验证 max_tokens 字段
	t.Run("body/max_tokens", func(t *testing.T) {
		maxTokens, ok := captured.Body["max_tokens"].(float64)
		if !ok {
			t.Fatalf("max_tokens should be a number, got %T: %v", captured.Body["max_tokens"], captured.Body["max_tokens"])
		}
		if maxTokens != 32000 {
			t.Errorf("max_tokens = %v, want %d", maxTokens, 32000)
		}
	})
}

// TestAnthropicTclaudeHeadersStableSessionID 验证同一进程内多次请求使用相同的 session ID。
func TestAnthropicTclaudeHeadersStableSessionID(t *testing.T) {
	var firstSID, secondSID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if firstSID == "" {
			firstSID = r.Header.Get("X-Claude-Code-Session-Id")
		} else {
			secondSID = r.Header.Get("X-Claude-Code-Session-Id")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"model":"m","usage":{}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	adapter := NewAnthropicAdapter()
	req := StreamRequest{
		BaseURL:                  server.URL,
		APIKey:                   "test-key",
		ProviderModelID:          "m",
		ModelID:                  "m",
		MaxTokens:                100,
		AnthropicMaxTokens:       100,
		AnthropicThinkingEffort:  "high",
		Messages:                 []Message{{Role: "user", Content: "hi"}},
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < 2; i++ {
		err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}

	if firstSID == "" || secondSID == "" {
		t.Fatalf("session IDs not captured: first=%q second=%q", firstSID, secondSID)
	}
	if firstSID != secondSID {
		t.Errorf("session ID should be stable within process: first=%q second=%q", firstSID, secondSID)
	}
}
