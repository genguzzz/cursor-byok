package modeladapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCodeBuddyHeaderValues 验证 CodeBuddy 请求头的值是否正确。
// Go 的 http.Header.Set() 会 canonicalize header name，所以本测试使用
// mock HTTP server 来验证 header 值（而非精确大小写）。
func TestCodeBuddyHeaderValues(t *testing.T) {
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          mockURL + "/v2",
		APIKey:           "ck_test_placeholder",
		ProviderModelID:  "deepseek-v4-pro-ioa",
		ModelID:          "deepseek-v4-pro-ioa",
		MaxTokens:        0,
		OpenAIEndpoint:   "/custom",
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "hello"},
		},
		OpenAIExtraParamsEnabled: true,
		OpenAIExtraParamsJSON:    CodeBuddyExtraParamsJSON(),
		CustomHeadersEnabled:     true,
		CustomHeadersJSON:        CodeBuddyHeadersJSON(),
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream returned error: %v", err)
	}

	// Go http.Header.Get 是大小写不敏感的，所以用 Get 来验证
	verifyHeader := func(t *testing.T, key string, expectedValue string) {
		t.Helper()
		val := captured.Headers.Get(key)
		if val == "" {
			// 列出所有 header 帮助 debug
			keys := make([]string, 0)
			for k := range captured.Headers {
				keys = append(keys, k)
			}
			t.Errorf("header %q not found. Available headers: %v", key, keys)
			return
		}
		if val != expectedValue {
			t.Errorf("header %q = %q, want %q", key, val, expectedValue)
		}
	}

	// CodeBuddy 特有的 header 值验证
	verifyHeader(t, "X-Requested-With", "XMLHttpRequest")
	verifyHeader(t, "X-Codebuddy-Request", "1")
	verifyHeader(t, "X-Ide-Type", "CLI")
	verifyHeader(t, "X-Ide-Name", "CLI")
	verifyHeader(t, "X-Ide-Version", CodeBuddyCLIVersion)
	verifyHeader(t, "X-Product", "SaaS")
	verifyHeader(t, "X-Agent-Intent", CodeBuddyAgentIntent)
	verifyHeader(t, "X-Private-Data", "false")
	verifyHeader(t, "X-Enterprise-Id", CodeBuddyDefaultEnterpriseID)
	verifyHeader(t, "X-Tenant-Id", CodeBuddyDefaultEnterpriseID)
	verifyHeader(t, "X-Domain", CodeBuddyDefaultDomain)
	verifyHeader(t, "X-Stainless-Arch", "arm64")
	verifyHeader(t, "X-Stainless-Lang", "js")
	verifyHeader(t, "X-Stainless-Os", "MacOS")
	verifyHeader(t, "X-Stainless-Package-Version", CodeBuddyStainlessVersion)
	verifyHeader(t, "X-Stainless-Runtime", CodeBuddyStainlessRuntime)
	verifyHeader(t, "X-Stainless-Runtime-Version", "v22.12.0")
	verifyHeader(t, "X-Stainless-Retry-Count", "0")
	verifyHeader(t, "User-Agent", CodeBuddyUserAgent)
	verifyHeader(t, "Accept", "application/json")
	verifyHeader(t, "Content-Type", "application/json")
}

// TestCodeBuddyBodyFormat 验证请求体格式与 CodeBuddy CLI 一致。
func TestCodeBuddyBodyFormat(t *testing.T) {
	var captured capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := json.Marshal(nil)
		// Read actual body for capture
		json.NewDecoder(r.Body).Decode(&captured.Body)
		// Also capture raw body
		_ = bodyBytes
		captured.Headers = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 返回标准的 OpenAI chat completion SSE 格式
		_, _ = w.Write([]byte("data: {\"id\":\"test-id\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v4-pro-ioa\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          server.URL + "/v2",
		APIKey:           "ck_test_placeholder",
		ProviderModelID:  "deepseek-v4-pro-ioa",
		ModelID:          "deepseek-v4-pro-ioa",
		MaxTokens:        0,
		OpenAIEndpoint:   "/custom",
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "hello"},
		},
		OpenAIExtraParamsEnabled: true,
		OpenAIExtraParamsJSON:    CodeBuddyExtraParamsJSON(),
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

	// 验证 model
	t.Run("body/model", func(t *testing.T) {
		model, ok := captured.Body["model"].(string)
		if !ok {
			t.Fatalf("model should be a string, got %T: %v", captured.Body["model"], captured.Body["model"])
		}
		if model != "deepseek-v4-pro-ioa" {
			t.Errorf("model = %q, want %q", model, "deepseek-v4-pro-ioa")
		}
	})

	// 验证 stream
	t.Run("body/stream", func(t *testing.T) {
		stream, ok := captured.Body["stream"].(bool)
		if !ok || !stream {
			t.Errorf("stream should be true, got %T: %v", captured.Body["stream"], captured.Body["stream"])
		}
	})

	// 验证 stream_options.include_usage
	t.Run("body/stream_options", func(t *testing.T) {
		so, ok := captured.Body["stream_options"].(map[string]any)
		if !ok {
			t.Fatalf("stream_options should be a map, got %T: %v", captured.Body["stream_options"], captured.Body["stream_options"])
		}
		if includeUsage, ok := so["include_usage"].(bool); !ok || !includeUsage {
			t.Errorf("stream_options.include_usage should be true, got %T: %v", so["include_usage"], so["include_usage"])
		}
	})

	// 验证 reasoning_summary（CodeBuddy 特有字段）
	t.Run("body/reasoning_summary", func(t *testing.T) {
		rs, ok := captured.Body["reasoning_summary"].(string)
		if !ok || rs != "auto" {
			t.Errorf("reasoning_summary should be \"auto\", got %T: %v", captured.Body["reasoning_summary"], captured.Body["reasoning_summary"])
		}
	})

	// 验证 messages 结构
	t.Run("body/messages", func(t *testing.T) {
		messages, ok := captured.Body["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("messages should be array with 2 elements, got %T len=%d", captured.Body["messages"], len(messages))
		}
		first, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("messages[0] should be a map")
		}
		if first["role"] != "system" {
			t.Errorf("messages[0].role = %q, want %q", first["role"], "system")
		}
	})
}

// TestCodeBuddyConfigExample 验证 CodeBuddy config.yaml 配置示例可以正常工作。
func TestCodeBuddyConfigExample(t *testing.T) {
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	// 模拟 config.yaml 中 CodeBuddy 的配置方式
	customHeadersJSON := CodeBuddyHeadersJSON()
	extraParamsJSON := CodeBuddyExtraParamsJSON()

	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          mockURL + "/v2",
		APIKey:           "ck_test_placeholder",
		ProviderModelID:  "deepseek-v4-flash-ioa",
		ModelID:          "deepseek-v4-flash-ioa",
		OpenAIEndpoint:   "/custom",
		Messages: []Message{
			{Role: "user", Content: "hi"},
		},
		OpenAIExtraParamsEnabled: true,
		OpenAIExtraParamsJSON:    extraParamsJSON,
		CustomHeadersEnabled:     true,
		CustomHeadersJSON:        customHeadersJSON,
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream returned error: %v", err)
	}

	// 验证 JSON 序列化/反序列化正确
	var parsedHeaders map[string]string
	if err := json.Unmarshal([]byte(customHeadersJSON), &parsedHeaders); err != nil {
		t.Fatalf("customHeadersJSON is not valid JSON: %v", err)
	}
	if parsedHeaders["X-IDE-Type"] != "CLI" {
		t.Errorf("X-IDE-Type = %q", parsedHeaders["X-IDE-Type"])
	}

	var parsedParams map[string]any
	if err := json.Unmarshal([]byte(extraParamsJSON), &parsedParams); err != nil {
		t.Fatalf("extraParamsJSON is not valid JSON: %v", err)
	}
	if parsedParams["reasoning_summary"] != "auto" {
		t.Errorf("reasoning_summary = %v", parsedParams["reasoning_summary"])
	}
}

// TestCodeBuddyURLConstruction 验证 /v2/chat/completions URL 构造。
func TestCodeBuddyURLConstruction(t *testing.T) {
	var captured capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Body = map[string]any{"_path": r.URL.Path}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	// 测试: baseURL 以 /v2 结尾 + /custom endpoint → 应追加 /chat/completions
	baseURL := server.URL + "/v2"
	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          baseURL,
		APIKey:           "ck_test",
		ProviderModelID:  "test-model",
		ModelID:          "test-model",
		OpenAIEndpoint:   "/custom",
		Messages:         []Message{{Role: "user", Content: "hi"}},
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = adapter.Stream(ctx, req, func(ModelEvent) error { return nil })

	if path, ok := captured.Body["_path"].(string); ok {
		if path != "/v2/chat/completions" {
			t.Errorf("request path = %q, want %q", path, "/v2/chat/completions")
		}
	} else {
		t.Error("failed to capture request path")
	}
}

// TestCodeBuddyReasoningSummaryPresent 验证 reasoning_summary 在所有
// CodeBuddy 模型中正确包含。
func TestCodeBuddyReasoningSummaryPresent(t *testing.T) {
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	for _, modelID := range CodeBuddyModelIDs() {
		t.Run("model/"+modelID, func(t *testing.T) {
			captured.Body = nil
			adapter := NewOpenAIAdapter()
			req := StreamRequest{
				BaseURL:          mockURL + "/v2",
				APIKey:           "ck_test",
				ProviderModelID:  modelID,
				ModelID:          modelID,
				OpenAIEndpoint:   "/custom",
				Messages:         []Message{{Role: "user", Content: "test"}},
				OpenAIExtraParamsEnabled: true,
				OpenAIExtraParamsJSON:    CodeBuddyExtraParamsJSON(),
				CustomHeadersEnabled:     true,
				CustomHeadersJSON:        CodeBuddyHeadersJSON(),
				ProviderStreamIdleTimeout: 30 * time.Second,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
			if err != nil {
				t.Fatalf("model %s: adapter.Stream error: %v", modelID, err)
			}

			rs, ok := captured.Body["reasoning_summary"].(string)
			if !ok || rs != "auto" {
				t.Errorf("model %s: reasoning_summary = %q, want \"auto\" (body keys: %v)",
					modelID, rs, mapKeys(captured.Body))
			}
		})
	}
}

// TestCodeBuddyRawHeaders 使用原始 TCP 抓包确认 codebuddy 的 header 确实
// 被发送到了 wire 上（即使 Go 做了 canonicalize）。
func TestCodeBuddyRawHeaders(t *testing.T) {
	var rawCapture rawCapturedRequest
	rawURL, rawClose := startRawTCPServer(t, &rawCapture)
	defer rawClose()

	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          rawURL + "/v2",
		APIKey:           "ck_test_placeholder",
		ProviderModelID:  "deepseek-v4-pro-ioa",
		ModelID:          "deepseek-v4-pro-ioa",
		MaxTokens:        0,
		OpenAIEndpoint:   "/custom",
		Messages: []Message{
			{Role: "user", Content: "test"},
		},
		OpenAIExtraParamsEnabled:  true,
		OpenAIExtraParamsJSON:     CodeBuddyExtraParamsJSON(),
		CustomHeadersEnabled:      true,
		CustomHeadersJSON:         CodeBuddyHeadersJSON(),
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream returned error: %v", err)
	}

	rawHeaders := parseRawHeaders(rawCapture.RawHeaders)

	// 使用 case-insensitive check 验证 header 值出现在 wire 上
	checkHeaderCaseInsensitive := func(t *testing.T, rawHeaders map[string]string, wantKey string, wantValue string) {
		t.Helper()
		wantLower := strings.ToLower(wantKey)
		found := false
		for key, val := range rawHeaders {
			if strings.ToLower(key) == wantLower {
				if val != wantValue {
					t.Errorf("header %q = %q, want %q (canonicalized form: %q)", key, val, wantValue, key)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("header %q not found in wire output.\nRaw headers:\n%s", wantKey, rawCapture.RawHeaders)
		}
	}

	checkHeaderCaseInsensitive(t, rawHeaders, "x-requested-with", "XMLHttpRequest")
	checkHeaderCaseInsensitive(t, rawHeaders, "x-codebuddy-request", "1")
	checkHeaderCaseInsensitive(t, rawHeaders, "x-ide-type", "CLI")
	checkHeaderCaseInsensitive(t, rawHeaders, "User-Agent", CodeBuddyUserAgent)
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}