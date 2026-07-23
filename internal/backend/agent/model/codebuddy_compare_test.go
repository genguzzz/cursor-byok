package modeladapter

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// proxyHeaders 是 Proxyman 抓到的 CodeBuddy CLI 实际请求头（flow 326 / 2057 共同部分）。
// 动态 UUID 和 trace ID 用占位符表示。
var proxyHeaders = map[string]string{
	"Accept":                      "application/json",
	"Content-Type":                "application/json",
	"x-requested-with":            "XMLHttpRequest",
	"x-stainless-arch":            "arm64",
	"x-stainless-lang":            "js",
	"x-stainless-os":              "MacOS",
	"x-stainless-package-version": "6.25.0",
	"x-stainless-retry-count":     "0",
	"x-stainless-runtime":         "node",
	"x-stainless-runtime-version": "v22.12.0",
	"X-Conversation-ID":           "<UUID>", // dynamic
	"X-Conversation-Request-ID":   "<UUID>", // dynamic
	"X-Agent-Intent":              "craft",
	"X-Agent-Purpose":             "prompt_suggestion", // only for sub-requests
	"X-IDE-Type":                  "CLI",
	"X-IDE-Name":                  "CLI",
	"X-IDE-Version":               "2.125.5",
	"X-Private-Data":              "false",
	"x-codebuddy-request":         "1",
	"X-Domain":                    "tencent.sso.copilot.tencent.com",
	"X-Enterprise-Id":             "etahzsqej0n4",
	"X-Tenant-Id":                 "etahzsqej0n4",
	"X-Request-ID":                "<UUID>", // dynamic
	"X-Conversation-Message-ID":   "<UUID>", // dynamic
	"Content-Encoding":            "gzip",
	"traceparent":                 "<DYNAMIC>", // W3C trace
	"b3":                          "<DYNAMIC>", // Zipkin
	"X-B3-TraceId":                "<DYNAMIC>", // Zipkin
	"X-B3-ParentSpanId":           "<DYNAMIC>", // Zipkin
	"X-B3-SpanId":                 "<DYNAMIC>", // Zipkin
	"X-B3-Sampled":                "<DYNAMIC>", // Zipkin
	"X-Trace-ID":                  "<DYNAMIC>", // Zipkin
	"Authorization":               "Bearer <REDACTED>",
	"X-User-Id":                   "2f6ed114-cbd8-4afd-b356-073fcc418391",
	"X-Product":                   "SaaS",
	"User-Agent":                  "CLI/2.125.5 CodeBuddy/2.125.5",
	"Host":                        "copilot.tencent.com",
	"Connection":                  "keep-alive",
}

// TestCodeBuddyCompareWithProxyMan 逐字段对比我们构造的请求与 Proxyman 抓包结果。
// 注意: HTTP header name 大小写不敏感。Go 的 http.Header 会 canonicalize，
// 所以 CodeBuddy CLI 的 X-IDE-Type 在我们的请求里会变成 X-Ide-Type。
// 本测试只对比 header VALUE，不做 key 大小写对比。
func TestCodeBuddyCompareWithProxyMan(t *testing.T) {
	// 用 mock HTTP server 抓取我们构造的请求
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          mockURL + "/v2",
		APIKey:           "ck_test_proxyman_compare",
		ProviderModelID:  "deepseek-v4-pro-ioa",
		ModelID:          "deepseek-v4-pro-ioa",
		OpenAIEndpoint:   "/custom",
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
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
		t.Fatalf("adapter.Stream error: %v", err)
	}

	t.Log("")
	t.Log(strings.Repeat("=", 70))
	t.Log("HEADER COMPARISON: Our Request vs CodeBuddy CLI (Proxyman)")
	t.Log("(case-insensitive key matching — Go canonicalizes header names)")
	t.Log(strings.Repeat("=", 70))

	ourHeaders := normalizeHeaders(captured.Headers)

	// 打印我们的所有 header
	t.Log("")
	t.Log("--- All headers in OUR request ---")
	for _, key := range sortedMapKeys(ourHeaders) {
		t.Logf("  %s: %s", key, ourHeaders[key])
	}

	// 逐字段对比（case-insensitive）
	t.Log("")
	t.Log("--- Field-by-field comparison (case-insensitive) ---")
	mismatches := 0
	dynamicSkipped := 0
	matched := 0

	proxyKeys := sortedMapKeys(proxyHeaders)
	for _, proxyKey := range proxyKeys {
		proxyVal := proxyHeaders[proxyKey]

		// 跳过动态字段
		if isDynamicPlaceholder(proxyVal) {
			dynamicSkipped++
			continue
		}
		// 跳过 gzip（我们的 Go 客户端不压缩 body）
		if proxyKey == "Content-Encoding" {
			t.Logf("  OK-SKIP: %s | CodeBuddy: gzip | Ours: (Go HTTP client, not compressed)", proxyKey)
			dynamicSkipped++
			continue
		}
		// 跳过 Auth（测试用不同 token）
		if proxyKey == "Authorization" {
			t.Logf("  OK-SKIP: %s | CodeBuddy: Bearer<redact> | Ours: Bearer ck_test_proxyman_compare", proxyKey)
			dynamicSkipped++
			continue
		}

		// case-insensitive 查找我们的 header
		found := false
		for ourKey, ourVal := range ourHeaders {
			if strings.EqualFold(ourKey, proxyKey) {
				if ourVal == proxyVal {
					t.Logf("  OK:      %s = %s (canonicalized as %s)", proxyKey, ourVal, ourKey)
					matched++
				} else {
					t.Errorf("  MISMATCH: %s | CodeBuddy: %s | Ours: %s (key: %s)",
						proxyKey, proxyVal, ourVal, ourKey)
					mismatches++
				}
				found = true
				break
			}
		}
		if !found {
			// 检查是否是我们有意不发的
			switch proxyKey {
			case "X-User-Id":
				t.Logf("  WARNING: X-User-Id not sent. This must be configured by user. CodeBuddy: %s", proxyVal)
			case "Connection", "Host":
				t.Logf("  OK-SKIP: %s | CodeBuddy: %s | Ours: (transport layer, Go HTTP manages)", proxyKey, proxyVal)
				dynamicSkipped++
			case "X-Agent-Purpose":
				t.Logf("  OK-SKIP: %s | CodeBuddy: %s | Ours: (prompt_suggestion only, not needed for main chat)", proxyKey, proxyVal)
				dynamicSkipped++
			default:
				t.Errorf("  MISSING: %s | CodeBuddy: %s | Ours: (NOT SENT)", proxyKey, proxyVal)
				mismatches++
			}
		}
	}

	t.Log("")
	t.Log(strings.Repeat("-", 70))
	t.Logf("SUMMARY: %d matched | %d skipped (dynamic/transport) | %d mismatched",
		matched, dynamicSkipped, mismatches)
	t.Log(strings.Repeat("-", 70))

	if mismatches > 0 {
		t.Fatalf("FAIL: %d header mismatches found (see above)", mismatches)
	}
}

// --- Body comparison ---

// proxyBodyKeys 是 Proxyman 抓到的 CodeBuddy CLI 请求体字段。
var proxyBodyKeys = []string{
	"model",
	"messages",
	"stream",
	"stream_options",
	"reasoning_summary",
}

// TestCodeBuddyBodyCompareWithProxyMan 验证请求体字段与 CodeBuddy CLI 一致。
func TestCodeBuddyBodyCompareWithProxyMan(t *testing.T) {
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	adapter := NewOpenAIAdapter()
	req := StreamRequest{
		BaseURL:          mockURL + "/v2",
		APIKey:           "ck_test",
		ProviderModelID:  "deepseek-v4-pro-ioa",
		ModelID:          "deepseek-v4-pro-ioa",
		OpenAIEndpoint:   "/custom",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
		OpenAIExtraParamsEnabled: true,
		OpenAIExtraParamsJSON:    CodeBuddyExtraParamsJSON(),
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream error: %v", err)
	}

	t.Log("")
	t.Log(strings.Repeat("=", 70))
	t.Log("BODY COMPARISON: Our Request vs CodeBuddy CLI (Proxyman)")
	t.Log(strings.Repeat("=", 70))

	// 验证所有必需字段存在
	for _, key := range proxyBodyKeys {
		val, exists := captured.Body[key]
		if !exists {
			t.Errorf("  MISSING: %s (CodeBuddy sends this)", key)
		} else {
			t.Logf("  PRESENT: %s = %v", key, val)
		}
	}

	// 验证 reason_summary 值为 "auto"
	if rs, ok := captured.Body["reasoning_summary"].(string); ok {
		if rs != "auto" {
			t.Errorf("  MISMATCH: reasoning_summary = %q, want \"auto\"", rs)
		} else {
			t.Logf("  OK: reasoning_summary = \"auto\"")
		}
	}

	// 验证 stream 为 true
	if stream, ok := captured.Body["stream"].(bool); ok {
		if !stream {
			t.Errorf("  MISMATCH: stream = false, want true")
		} else {
			t.Logf("  OK: stream = true")
		}
	}

	// 验证 stream_options.include_usage
	if so, ok := captured.Body["stream_options"].(map[string]any); ok {
		if iu, ok := so["include_usage"].(bool); ok && iu {
			t.Logf("  OK: stream_options.include_usage = true")
		} else {
			t.Errorf("  MISMATCH: stream_options.include_usage = %v, want true", iu)
		}
	}

	// 检查我们多了什么字段
	t.Log("")
	t.Log("--- Extra body fields in our request ---")
	for key, val := range captured.Body {
		found := false
		for _, pk := range proxyBodyKeys {
			if key == pk {
				found = true
				break
			}
		}
		if !found {
			t.Logf("  EXTRA: %s = %v", key, val)
		}
	}
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isDynamicPlaceholder(val string) bool {
	return val == "<UUID>" || val == "<DYNAMIC>" || val == "<REDACTED>"
}

func isTransportHeader(key string) bool {
	switch key {
	case "Host", "Connection", "Content-Length", "Accept-Encoding":
		return true
	}
	return strings.HasPrefix(key, "X-B3-") || key == "b3" || key == "traceparent" || key == "X-Trace-ID" ||
		key == "X-Conversation-ID" || key == "X-Conversation-Request-ID" ||
		key == "X-Request-ID" || key == "X-Conversation-Message-ID" ||
		key == "X-Agent-Purpose"
}

func normalizeHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			result[key] = values[0]
		}
	}
	return result
}