package modeladapter

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

// rawCapturedRequest 保存从 TCP 层抓取的原始 HTTP 请求文本，
// 用于验证 header key 的精确大小写（Go http.Header 会做 canonical 化）。
type rawCapturedRequest struct {
	RawHeaders string
	Body       map[string]any
}

// startRawTCPServer 启动一个原始 TCP listener，抓取 HTTP 请求的原始字节，
// 绕过 Go 的 textproto.CanonicalMIMEHeaderKey 行为。
// 返回 server URL 和 关闭函数。
func startRawTCPServer(t *testing.T, capture *rawCapturedRequest) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().String()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		// 读取请求行（丢弃，只关心 header）
		_, err = reader.ReadString('\n')
		if err != nil {
			return
		}
		// 读取所有 header 行直到空行
		var headerLines []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			headerLines = append(headerLines, line)
		}
		capture.RawHeaders = strings.Join(headerLines, "\n")

		// 读取 body（根据 Content-Length）
		contentLength := 0
		for _, hl := range headerLines {
			if strings.HasPrefix(strings.ToLower(hl), "content-length:") {
				cl := strings.TrimSpace(strings.SplitN(hl, ":", 2)[1])
				if _, err := fmt.Sscanf(cl, "%d", &contentLength); err == nil {
					break
				}
			}
		}
		bodyBytes := make([]byte, contentLength)
		if contentLength > 0 {
			if _, err := io.ReadFull(reader, bodyBytes); err != nil {
				return
			}
		}
		for _, hl := range headerLines {
			if strings.HasPrefix(strings.ToLower(hl), "content-encoding:") &&
				strings.Contains(strings.ToLower(hl), "gzip") {
				reader, err := gzip.NewReader(bytes.NewReader(bodyBytes))
				if err == nil {
					decoded, readErr := io.ReadAll(reader)
					_ = reader.Close()
					if readErr == nil {
						bodyBytes = decoded
					}
				}
				break
			}
		}
		var bodyMap map[string]any
		_ = json.Unmarshal(bodyBytes, &bodyMap)
		capture.Body = bodyMap

		// 返回最小化 SSE 响应（正确的 chunked 编码）
		sseData := "event: message_start\r\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"model\":\"test-model\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\r\n" +
			"\r\n" +
			"event: message_stop\r\n" +
			"data: {\"type\":\"message_stop\"}\r\n" +
			"\r\n"
		chunkHex := fmt.Sprintf("%x", len(sseData))
		response := "HTTP/1.1 200 OK\r\n" +
			"Content-Type: text/event-stream\r\n" +
			"Transfer-Encoding: chunked\r\n" +
			"Connection: close\r\n" +
			"\r\n" +
			chunkHex + "\r\n" + sseData + "\r\n" +
			"0\r\n\r\n"
		_, _ = conn.Write([]byte(response))
	}()

	return "http://" + addr, func() {
		ln.Close()
		<-done
	}
}

// startMockSSEServer 启动一个标准 httptest server，用于 body 验证。
func startMockSSEServer(t *testing.T, capture *capturedRequest) (string, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
			reader, err := gzip.NewReader(bytes.NewReader(bodyBytes))
			if err == nil {
				decoded, readErr := io.ReadAll(reader)
				_ = reader.Close()
				if readErr == nil {
					bodyBytes = decoded
				}
			}
		}
		var bodyMap map[string]any
		_ = json.Unmarshal(bodyBytes, &bodyMap)
		capture.Headers = r.Header.Clone()
		capture.Body = bodyMap
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"model":"test-model","usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	return server.URL, server.Close
}

// TestAnthropicTclaudeHeaderRawCase 使用原始 TCP 抓包验证 header key 的精确大小写，
// 确保与 claude-code CLI 发出的请求在 wire 层面完全一致。
func TestAnthropicTclaudeHeaderRawCase(t *testing.T) {
	var rawCapture rawCapturedRequest
	rawURL, rawClose := startRawTCPServer(t, &rawCapture)
	defer rawClose()

	adapter := NewAnthropicAdapter()
	req := StreamRequest{
		BaseURL:                  rawURL + "/v1/messages?beta=true",
		APIKey:                   "Bearer placeholder",
		ProviderModelID:          "claude-test-model",
		ModelID:                  "claude-test-model",
		MaxTokens:                32000,
		AnthropicMaxTokens:       32000,
		AnthropicThinkingEffort:  "high",
		Messages:                 []Message{{Role: "user", Content: "hello"}},
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream returned error: %v", err)
	}

	// 解析 raw header 为 key→value map（保留原始大小写）
	rawHeaders := parseRawHeaders(rawCapture.RawHeaders)

	// 定义期望的 raw header key（与 claude-code CLI 抓包对比结果一致）
	expectedRawKeys := map[string]string{
		"Accept":                                "application/json",
		"Authorization":                         "Bearer placeholder",
		"User-Agent":                            "claude-cli/2.1.154 (external, cli)",
		"anthropic-version":                     "2023-06-01",
		"anthropic-beta":                        "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24",
		"anthropic-dangerous-direct-browser-access": "true",
		"content-type":                          "application/json",
		"x-app":                                 "cli",
		"X-Claude-Code-Session-Id":              "",  // 只验证存在
		"X-Stainless-Arch":                      "",  // 只验证存在
		"X-Stainless-Lang":                      "js",
		"X-Stainless-OS":                        "MacOS",
		"X-Stainless-Package-Version":           "0.94.0",
		"X-Stainless-Retry-Count":               "0",
		"X-Stainless-Runtime":                   "node",
		"X-Stainless-Runtime-Version":           "v24.3.0",
		"X-Stainless-Timeout":                   "600",
		"Accept-Encoding":                       "gzip, deflate, br, zstd",
		"Connection":                            "keep-alive",
	}

	for key, expectedVal := range expectedRawKeys {
		t.Run("raw-header/"+key, func(t *testing.T) {
			val, exists := rawHeaders[key]
			if !exists {
				t.Errorf("expected raw header key %q not found in wire output.\nRaw headers:\n%s", key, rawCapture.RawHeaders)
				return
			}
			if expectedVal != "" && val != expectedVal {
				t.Errorf("raw header %q = %q, want %q", key, val, expectedVal)
			}
		})
	}

	// 验证不存在 canonical 化的重复 key
	canonicalDupes := []string{
		"Anthropic-Beta",
		"Anthropic-Version",
		"Anthropic-Dangerous-Direct-Browser-Access",
		"X-App",
		"X-Stainless-Os",
	}
	for _, dup := range canonicalDupes {
		t.Run("no-canonical-dupe/"+dup, func(t *testing.T) {
			if _, exists := rawHeaders[dup]; exists {
				t.Errorf("canonical header %q should not exist (raw lowercase version should be used instead)", dup)
			}
		})
	}

	// 验证 x-api-key 不存在
	t.Run("no-x-api-key", func(t *testing.T) {
		for k := range rawHeaders {
			if strings.EqualFold(k, "x-api-key") {
				t.Errorf("x-api-key header should not be present, found: %q", k)
			}
		}
	})
}

// TestAnthropicTclaudeBodyAlignment 通过 mock HTTP server 验证请求体字段对齐。
func TestAnthropicTclaudeBodyAlignment(t *testing.T) {
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	adapter := NewAnthropicAdapter()
	req := StreamRequest{
		BaseURL:                  mockURL + "/v1/messages?beta=true",
		APIKey:                   "Bearer placeholder",
		ProviderModelID:          "claude-test-model",
		ModelID:                  "claude-test-model",
		MaxTokens:                32000,
		AnthropicMaxTokens:       32000,
		AnthropicThinkingEffort:  "high",
		Messages:                 []Message{{Role: "user", Content: "hello"}},
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

	// 验证 thinking 字段
	t.Run("body/thinking", func(t *testing.T) {
		thinking, ok := captured.Body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("thinking should be a map, got %T: %v", captured.Body["thinking"], captured.Body["thinking"])
		}
		if thinking["type"] != "adaptive" {
			t.Errorf("thinking.type = %v, want %q", thinking["type"], "adaptive")
		}
	})

	// 验证 output_config 字段
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

	// 验证 metadata.user_id 字段
	t.Run("body/metadata.user_id", func(t *testing.T) {
		meta, ok := captured.Body["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("metadata should be a map, got %T: %v", captured.Body["metadata"], captured.Body["metadata"])
		}
		userID, ok := meta["user_id"].(string)
		if !ok {
			t.Fatalf("metadata.user_id should be a string, got %T: %v", meta["user_id"], meta["user_id"])
		}
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

// parseRawHeaders 将原始 header 行解析为 key→value map，保留 key 的原始大小写。
func parseRawHeaders(raw string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		result[key] = val
	}
	return result
}