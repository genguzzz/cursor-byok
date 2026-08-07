// codebuddy.go 提供 CodeBuddy CLI 兼容的请求头和请求体辅助函数。
//
// CodeBuddy CLI (v2.127.0) 通过 copilot.tencent.com 的 OpenAI 兼容端点
// 使用 DeepSeek 等模型，请求需要携带特定的认证、追踪和兼容性头。
// 本文件将这些头定义为一组可复用的常量与函数，供适配器内部使用，
// 也可在 config.yaml 中通过 customHeadersJSON 引用。
package modeladapter

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cursor/internal/netproxy"

	"github.com/google/uuid"
)

// CodeBuddy CLI 客户端常量 —— 与 Proxyman flow 2000（CLI v2.127.0）对齐。
const (
	CodeBuddyCLIVersion          = "2.127.0"
	CodeBuddyStainlessVersion    = "6.25.0"
	CodeBuddyStainlessRuntime    = "node"
	CodeBuddyNodeVersion         = "v23.11.1"
	CodeBuddyUserAgent           = "CLI/2.127.0 CodeBuddy/2.127.0"
	CodeBuddyAgentIntent         = "craft"
	CodeBuddyAgentPurpose        = "conversation"
	CodeBuddyDefaultEnterpriseID = "etahzsqej0n4"
	CodeBuddyDefaultDomain       = "tencent.sso.copilot.tencent.com"

	// CodeBuddyAPIBaseV2 是 copilot.tencent.com 的 /v2 基础 URL。
	CodeBuddyAPIBaseV2 = "https://copilot.tencent.com/v2"
)

// CodeBuddyStandardHeaders 返回 CodeBuddy CLI 发出的静态标准请求头
// （不含 Authorization、X-User-Id 与按请求生成的 conversation/trace 头）。
func CodeBuddyStandardHeaders() map[string]string {
	return map[string]string{
		"Accept":                      "application/json",
		"Content-Type":                "application/json",
		"x-requested-with":            "XMLHttpRequest",
		"User-Agent":                  CodeBuddyUserAgent,
		"X-IDE-Type":                  "CLI",
		"X-IDE-Name":                  "CLI",
		"X-IDE-Version":               CodeBuddyCLIVersion,
		"X-Product":                   "SaaS",
		"X-Agent-Intent":              CodeBuddyAgentIntent,
		"X-Agent-Purpose":             CodeBuddyAgentPurpose,
		"X-Private-Data":              "false",
		"X-Enterprise-Id":             CodeBuddyDefaultEnterpriseID,
		"X-Tenant-Id":                 CodeBuddyDefaultEnterpriseID,
		"X-Domain":                    CodeBuddyDefaultDomain,
		"x-stainless-arch":            "arm64",
		"x-stainless-lang":            "js",
		"x-stainless-os":              "MacOS",
		"x-stainless-package-version": CodeBuddyStainlessVersion,
		"x-stainless-runtime":         CodeBuddyStainlessRuntime,
		"x-stainless-runtime-version": CodeBuddyNodeVersion,
		"x-stainless-retry-count":     "0",
	}
}

// CodeBuddyHeadersJSON 返回 CodeBuddyStandardHeaders 的 JSON 字符串。
func CodeBuddyHeadersJSON() string {
	payload, _ := json.Marshal(CodeBuddyStandardHeaders())
	return string(payload)
}

// CodeBuddyFullHeaders 返回包含所有必需 header 的 map（含 X-User-Id，不含 Authorization）。
func CodeBuddyFullHeaders(xUserID string) map[string]string {
	headers := CodeBuddyStandardHeaders()
	if xUserID != "" {
		headers["X-User-Id"] = xUserID
	}
	return headers
}

// CodeBuddyFullHeadersJSON 返回包含 X-User-Id 的完整 headers JSON。
func CodeBuddyFullHeadersJSON(xUserID string) string {
	payload, _ := json.Marshal(CodeBuddyFullHeaders(xUserID))
	return string(payload)
}

// ApplyCodeBuddyHeaders 将 CodeBuddy CLI 标准请求头写入 http.Request。
func ApplyCodeBuddyHeaders(httpReq *http.Request) {
	for key, value := range CodeBuddyStandardHeaders() {
		httpReq.Header.Set(key, value)
	}
}

// CodeBuddyExtraParams 返回 CodeBuddy 出站请求体中的稳定额外字段。
// temperature / verbosity 不写死：verbosity 由本轮 ReasoningEffort（Cursor 思考强度）映射；
// temperature 仅在用户 openAIExtraParams 或上游显式配置时下发。
func CodeBuddyExtraParams() map[string]any {
	return map[string]any{
		"reasoning_summary": "auto",
	}
}

// CodeBuddyExtraParamsJSON 返回 CodeBuddyExtraParams 的 JSON 字符串。
func CodeBuddyExtraParamsJSON() string {
	payload, _ := json.Marshal(CodeBuddyExtraParams())
	return string(payload)
}

// codeBuddyVerbosityFromEffort 把 Cursor/渠道侧 reasoning effort 映射为 CodeBuddy verbosity。
// 未识别或 disabled 时返回空，表示不下发该字段。
func codeBuddyVerbosityFromEffort(effort string) string {
	switch normalizeRuntimeThinkingEffort(effort) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max":
		return "high"
	default:
		return ""
	}
}

// CodeBuddyModelIDs 返回已知的 CodeBuddy 可用模型 ID 列表。
func CodeBuddyModelIDs() []string {
	return []string{
		"deepseek-v4-pro-ioa",
		"deepseek-v4-flash-ioa",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"claude-sonnet-5-1m",
	}
}

// CodeBuddyAdapter 封装 OpenAI 适配器，自动注入 CodeBuddy 特有的请求头和请求体参数。
type CodeBuddyAdapter struct {
	openai *OpenAIAdapter
}

// NewCodeBuddyAdapter 创建 CodeBuddy 适配器。
func NewCodeBuddyAdapter() *CodeBuddyAdapter {
	return &CodeBuddyAdapter{
		openai: NewOpenAIAdapter(),
	}
}

// Stream 实现 ModelAdapter 接口，自动注入 CodeBuddy 请求头后将请求委托给 OpenAIAdapter。
func (a *CodeBuddyAdapter) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	mergedHeaders := CodeBuddyStandardHeaders()
	if req.CustomHeadersEnabled && strings.TrimSpace(req.CustomHeadersJSON) != "" {
		var userHeaders map[string]string
		if err := json.Unmarshal([]byte(req.CustomHeadersJSON), &userHeaders); err == nil {
			for key, value := range userHeaders {
				if strings.TrimSpace(key) != "" {
					mergedHeaders[key] = value
				}
			}
		}
	}
	applyCodeBuddyDynamicHeaders(mergedHeaders, req)
	ensureCodeBuddyUserID(mergedHeaders, req.APIKey)
	if mergedBytes, err := json.Marshal(mergedHeaders); err == nil {
		req.CustomHeadersJSON = string(mergedBytes)
	}
	req.CustomHeadersEnabled = true

	req.OpenAIExtraParamsJSON = mergeCodeBuddyExtraParamsJSON(req.OpenAIExtraParamsEnabled, req.OpenAIExtraParamsJSON, req.ReasoningEffort)
	req.OpenAIExtraParamsEnabled = true
	req.GzipRequestBody = true

	return a.openai.Stream(ctx, req, sink)
}

func mergeCodeBuddyExtraParamsJSON(userEnabled bool, userJSON string, reasoningEffort string) string {
	merged := CodeBuddyExtraParams()
	if userEnabled && strings.TrimSpace(userJSON) != "" {
		var user map[string]any
		if json.Unmarshal([]byte(userJSON), &user) == nil {
			for key, value := range user {
				name := strings.TrimSpace(key)
				if name == "" {
					continue
				}
				merged[name] = value
			}
		}
	}
	if _, hasVerbosity := merged["verbosity"]; !hasVerbosity {
		if verbosity := codeBuddyVerbosityFromEffort(reasoningEffort); verbosity != "" {
			merged["verbosity"] = verbosity
		}
	}
	payload, err := json.Marshal(merged)
	if err != nil {
		return CodeBuddyExtraParamsJSON()
	}
	return string(payload)
}

// applyCodeBuddyDynamicHeaders 注入 CLI 每次请求都会带的 conversation / request / tracing 头。
// 已由用户 customHeaders 显式设置的 key 不覆盖。
func applyCodeBuddyDynamicHeaders(headers map[string]string, req StreamRequest) {
	if headers == nil {
		return
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = uuid.NewString()
	}
	conversationRequestID := codeBuddyCompactID(req.RequestID)
	if conversationRequestID == "" {
		conversationRequestID = codeBuddyRandomHex(16)
	}
	messageID := codeBuddyCompactID(req.ModelCallID)
	if messageID == "" {
		messageID = codeBuddyRandomHex(16)
	}

	setIfAbsent(headers, "X-Conversation-ID", conversationID)
	setIfAbsent(headers, "X-Conversation-Request-ID", conversationRequestID)
	setIfAbsent(headers, "X-Request-ID", messageID)
	setIfAbsent(headers, "X-Conversation-Message-ID", messageID)

	traceID := codeBuddyRandomHex(16)
	spanID := codeBuddyRandomHex(8)
	parentSpanID := codeBuddyRandomHex(8)
	setIfAbsent(headers, "traceparent", fmt.Sprintf("00-%s-%s-01", traceID, spanID))
	setIfAbsent(headers, "b3", fmt.Sprintf("%s-%s-1-%s", traceID, spanID, parentSpanID))
	setIfAbsent(headers, "X-B3-TraceId", traceID)
	setIfAbsent(headers, "X-B3-ParentSpanId", parentSpanID)
	setIfAbsent(headers, "X-B3-SpanId", spanID)
	setIfAbsent(headers, "X-B3-Sampled", "1")
	setIfAbsent(headers, "X-Trace-ID", traceID)
}

func ensureCodeBuddyUserID(headers map[string]string, apiKey string) {
	if headers == nil {
		return
	}
	if headerValueCI(headers, "X-User-Id") != "" {
		return
	}
	if userID := codeBuddyUserIDFromAPIKey(apiKey); userID != "" {
		headers["X-User-Id"] = userID
	}
}

func headerValueCI(headers map[string]string, want string) string {
	for key, value := range headers {
		if strings.EqualFold(key, want) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func setIfAbsent(headers map[string]string, key, value string) {
	if headerValueCI(headers, key) != "" {
		return
	}
	headers[key] = value
}

func codeBuddyCompactID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return strings.ReplaceAll(trimmed, "-", "")
}

func codeBuddyRandomHex(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 16
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		// 退化路径：仍给出固定长度伪随机，避免请求缺头。
		fallback := uuid.New()
		return strings.ReplaceAll(fallback.String(), "-", "")[:byteLen*2]
	}
	return hex.EncodeToString(buf)
}

// codeBuddyUserIDFromAPIKey 从 JWT access token 的 sub 提取 X-User-Id（与 CLI 对齐）。
func codeBuddyUserIDFromAPIKey(apiKey string) string {
	token := strings.TrimSpace(apiKey)
	token = strings.TrimPrefix(token, "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		padded := parts[1]
		if m := len(padded) % 4; m != 0 {
			padded += strings.Repeat("=", 4-m)
		}
		payload, err = base64.URLEncoding.DecodeString(padded)
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

// CodeBuddyModelInfo 表示从 /v3/config 返回的模型信息。
type CodeBuddyModelInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	MaxInputTokens    int    `json:"maxInputTokens"`
	MaxOutputTokens   int    `json:"maxOutputTokens"`
	SupportsReasoning bool   `json:"supportsReasoning"`
}

// CodeBuddyConfigResponse 表示 /v3/config 的响应结构。
type CodeBuddyConfigResponse struct {
	Code int                 `json:"code"`
	Msg  string              `json:"msg"`
	Data CodeBuddyConfigData `json:"data"`
}

// CodeBuddyConfigData 包含 models 列表。
type CodeBuddyConfigData struct {
	Models []CodeBuddyModelInfo `json:"models"`
}

// CodeBuddyModelDiscovery 用于从 copilot.tencent.com/v3/config 获取可用模型列表。
type CodeBuddyModelDiscovery struct {
	mu       sync.Mutex
	timeout  time.Duration
	cacheTTL time.Duration
	cachedAt time.Time
	cached   []CodeBuddyModelInfo
}

// NewCodeBuddyModelDiscovery 创建模型发现客户端。
func NewCodeBuddyModelDiscovery() *CodeBuddyModelDiscovery {
	return &CodeBuddyModelDiscovery{
		timeout:  15 * time.Second,
		cacheTTL: 5 * time.Minute,
	}
}

func discoveryClientForProxy(proxyURL string, timeout time.Duration) *http.Client {
	if client := netproxy.NewHTTPClient(timeout); client != nil {
		proxyURL = strings.TrimSpace(proxyURL)
		if proxyURL == "" {
			return client
		}
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return client
		}
		baseTransport, _ := client.Transport.(*http.Transport)
		transport := &http.Transport{
			Proxy: http.ProxyURL(parsed),
		}
		if baseTransport != nil {
			transport.DialContext = baseTransport.DialContext
			transport.ForceAttemptHTTP2 = baseTransport.ForceAttemptHTTP2
			transport.TLSHandshakeTimeout = baseTransport.TLSHandshakeTimeout
			transport.ExpectContinueTimeout = baseTransport.ExpectContinueTimeout
			transport.MaxIdleConns = baseTransport.MaxIdleConns
			transport.IdleConnTimeout = baseTransport.IdleConnTimeout
		}
		return &http.Client{
			Transport: transport,
			Timeout:   timeout,
		}
	}
	return &http.Client{Timeout: timeout}
}

// FetchModels 从 /v3/config 获取可用模型列表（带缓存）。
func (d *CodeBuddyModelDiscovery) FetchModels(ctx context.Context, apiKey string, xUserID string, proxyURL string) ([]CodeBuddyModelInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if time.Since(d.cachedAt) < d.cacheTTL && len(d.cached) > 0 {
		return d.cached, nil
	}

	models, err := d.fetchFromAPI(ctx, apiKey, xUserID, proxyURL)
	if err != nil {
		if len(d.cached) > 0 {
			return d.cached, nil
		}
		return nil, err
	}

	d.cached = models
	d.cachedAt = time.Now()
	return models, nil
}

func (d *CodeBuddyModelDiscovery) fetchFromAPI(ctx context.Context, apiKey string, xUserID string, proxyURL string) ([]CodeBuddyModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://copilot.tencent.com/v3/config", nil)
	if err != nil {
		return nil, fmt.Errorf("codebuddy model discovery: create request: %w", err)
	}

	apiKey = strings.TrimSpace(apiKey)
	if !strings.HasPrefix(apiKey, "Bearer ") {
		apiKey = "Bearer " + apiKey
	}
	req.Header.Set("Authorization", apiKey)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", CodeBuddyUserAgent)
	req.Header.Set("X-User-Id", strings.TrimSpace(xUserID))
	req.Header.Set("X-Enterprise-Id", CodeBuddyDefaultEnterpriseID)
	req.Header.Set("X-Tenant-Id", CodeBuddyDefaultEnterpriseID)
	req.Header.Set("X-Domain", CodeBuddyDefaultDomain)
	req.Header.Set("X-Product", "SaaS")

	client := discoveryClientForProxy(proxyURL, d.timeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy model discovery: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy model discovery: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy model discovery: http status %d: %s", resp.StatusCode, string(body))
	}

	var response CodeBuddyConfigResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("codebuddy model discovery: parse response: %w", err)
	}

	if response.Code != 0 {
		return nil, fmt.Errorf("codebuddy model discovery: api error code=%d msg=%s", response.Code, response.Msg)
	}

	return response.Data.Models, nil
}
