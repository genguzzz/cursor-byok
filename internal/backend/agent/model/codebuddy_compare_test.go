package modeladapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// proxyHeaders 是 Proxyman 抓到的 CodeBuddy CLI 实际请求头（flow 2000，CLI v2.127.0）。
// 动态 UUID / trace ID 用占位符表示；测试通过 CodeBuddyAdapter 校验值格式与是否缺失。
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
	"x-stainless-runtime-version": "v23.11.1",
	"X-Conversation-ID":           "<UUID>",
	"X-Conversation-Request-ID":   "<UUID>",
	"X-Agent-Intent":              "craft",
	"X-Agent-Purpose":             "conversation",
	"X-IDE-Type":                  "CLI",
	"X-IDE-Name":                  "CLI",
	"X-IDE-Version":               "2.127.0",
	"X-Private-Data":              "false",
	"x-codebuddy-request":         "1",
	"X-Domain":                    "tencent.sso.copilot.tencent.com",
	"X-Enterprise-Id":             "etahzsqej0n4",
	"X-Tenant-Id":                 "etahzsqej0n4",
	"X-Request-ID":                "<UUID>",
	"X-Conversation-Message-ID":   "<UUID>",
	"Content-Encoding":            "gzip",
	"traceparent":                 "<DYNAMIC>",
	"b3":                          "<DYNAMIC>",
	"X-B3-TraceId":                "<DYNAMIC>",
	"X-B3-ParentSpanId":           "<DYNAMIC>",
	"X-B3-SpanId":                 "<DYNAMIC>",
	"X-B3-Sampled":                "1",
	"X-Trace-ID":                  "<DYNAMIC>",
	"Authorization":               "Bearer <REDACTED>",
	"X-User-Id":                   "2f6ed114-cbd8-4afd-b356-073fcc418391",
	"X-Product":                   "SaaS",
	"User-Agent":                  "CLI/2.127.0 CodeBuddy/2.127.0",
	"Host":                        "copilot.tencent.com",
	"Connection":                  "keep-alive",
}

// TestCodeBuddyCompareWithProxyMan 逐字段对比我们构造的请求与 Proxyman 抓包结果。
func TestCodeBuddyCompareWithProxyMan(t *testing.T) {
	var captured capturedRequest
	mockURL, mockClose := startMockSSEServer(t, &captured)
	defer mockClose()

	adapter := NewCodeBuddyAdapter()
	req := StreamRequest{
		BaseURL:         mockURL + "/v2",
		APIKey:          codeBuddyTestJWT("2f6ed114-cbd8-4afd-b356-073fcc418391"),
		ProviderModelID: "deepseek-v4-pro-ioa",
		ModelID:         "deepseek-v4-pro-ioa",
		OpenAIEndpoint:  "/custom",
		ConversationID:  "6e364c3e-3cfe-491a-8366-93c6d619ecea",
		RequestID:       "8780fcaf-5ec2-4ea4-b5e9-51d9d4dce3b8",
		ModelCallID:     "7b656ee9-fb7f-4a0d-a342-4df222773030",
		Messages: []Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "hello"},
		},
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream error: %v", err)
	}

	ourHeaders := normalizeHeaders(captured.Headers)
	mismatches := 0
	dynamicSkipped := 0
	matched := 0

	for _, proxyKey := range sortedMapKeys(proxyHeaders) {
		proxyVal := proxyHeaders[proxyKey]

		if proxyKey == "Authorization" || proxyKey == "Connection" || proxyKey == "Host" {
			dynamicSkipped++
			continue
		}

		_, ourVal, found := findHeaderCI(ourHeaders, proxyKey)
		if !found {
			t.Errorf("  MISSING: %s | CodeBuddy: %s | Ours: (NOT SENT)", proxyKey, proxyVal)
			mismatches++
			continue
		}

		if isDynamicPlaceholder(proxyVal) {
			if strings.TrimSpace(ourVal) == "" {
				t.Errorf("  EMPTY DYNAMIC: %s present but empty", proxyKey)
				mismatches++
			} else {
				matched++
			}
			continue
		}

		if ourVal == proxyVal {
			matched++
		} else {
			t.Errorf("  MISMATCH: %s | CodeBuddy: %s | Ours: %s", proxyKey, proxyVal, ourVal)
			mismatches++
		}
	}

	if mismatches > 0 {
		t.Fatalf("FAIL: %d header mismatches found", mismatches)
	}
	if matched == 0 {
		t.Fatal("no headers matched")
	}
	t.Logf("SUMMARY: %d matched | %d skipped | %d mismatched", matched, dynamicSkipped, mismatches)

	if ae := captured.Headers.Get("Accept-Encoding"); ae != "" {
		t.Errorf("unexpected Accept-Encoding=%q (CLI does not send this)", ae)
	}
}

// proxyBodyKeys 是 CodeBuddy 出站 body 的稳定字段子集。
// temperature / verbosity 由调用端决定，不作为固定必有字段断言。
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

	adapter := NewCodeBuddyAdapter()
	req := StreamRequest{
		BaseURL:         mockURL + "/v2",
		APIKey:          "ck_test",
		ProviderModelID: "deepseek-v4-pro-ioa",
		ModelID:         "deepseek-v4-pro-ioa",
		OpenAIEndpoint:  "/custom",
		ReasoningEffort: "high",
		Messages: []Message{
			{Role: "user", Content: "hello"},
		},
		ProviderStreamIdleTimeout: 30 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := adapter.Stream(ctx, req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("adapter.Stream error: %v", err)
	}

	for _, key := range proxyBodyKeys {
		val, exists := captured.Body[key]
		if !exists {
			t.Errorf("  MISSING: %s (CodeBuddy sends this)", key)
		} else {
			t.Logf("  PRESENT: %s = %v", key, val)
		}
	}

	if rs, ok := captured.Body["reasoning_summary"].(string); !ok || rs != "auto" {
		t.Errorf("reasoning_summary = %v, want auto", captured.Body["reasoning_summary"])
	}
	if verbosity, ok := captured.Body["verbosity"].(string); !ok || verbosity != "high" {
		t.Errorf("verbosity = %v, want high (from ReasoningEffort)", captured.Body["verbosity"])
	}
	if _, hasTemp := captured.Body["temperature"]; hasTemp {
		t.Errorf("temperature should not be hardcoded, got %v", captured.Body["temperature"])
	}
	if stream, ok := captured.Body["stream"].(bool); !ok || !stream {
		t.Errorf("stream = %v, want true", captured.Body["stream"])
	}
	if so, ok := captured.Body["stream_options"].(map[string]any); ok {
		if iu, ok := so["include_usage"].(bool); !ok || !iu {
			t.Errorf("stream_options.include_usage = %v, want true", so["include_usage"])
		}
	} else {
		t.Errorf("stream_options missing")
	}

	if enc := captured.Headers.Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", enc)
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

func normalizeHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			result[key] = values[0]
		}
	}
	return result
}

func findHeaderCI(headers map[string]string, want string) (string, string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, want) {
			return key, value, true
		}
	}
	return "", "", false
}

func codeBuddyTestJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(map[string]any{"sub": sub})
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".sig"
}
