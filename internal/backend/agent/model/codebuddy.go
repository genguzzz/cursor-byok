// codebuddy.go 提供 CodeBuddy CLI 兼容的请求头和请求体辅助函数。
//
// CodeBuddy CLI (v2.125.5) 通过 copilot.tencent.com 的 OpenAI 兼容端点
// 使用 DeepSeek 等模型，请求需要携带特定的认证、追踪和兼容性头。
// 本文件将这些头定义为一组可复用的常量与函数，供适配器内部使用，
// 也可在 config.yaml 中通过 customHeadersJSON 引用。
package modeladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cursor/internal/netproxy"
)

// CodeBuddy CLI 客户端常量 —— 与 v2.125.5 版本对齐。
const (
	CodeBuddyCLIVersion          = "2.126.0"
	CodeBuddyStainlessVersion    = "6.25.0"
	CodeBuddyStainlessRuntime    = "node"
	CodeBuddyNodeVersion         = "v23.11.1"
	CodeBuddyUserAgent           = "CLI/2.126.0 CodeBuddy/2.126.0"
	CodeBuddyAgentIntent         = "craft"
	CodeBuddyDefaultEnterpriseID = "etahzsqej0n4"
	CodeBuddyDefaultDomain       = "tencent.sso.copilot.tencent.com"

	// CodeBuddyAPIBaseV2 是 copilot.tencent.com 的 /v2 基础 URL。
	CodeBuddyAPIBaseV2 = "https://copilot.tencent.com/v2"
)

// CodeBuddyStandardHeaders 返回 CodeBuddy CLI 发出的所有标准请求头
// （不含 Authorization 和 X-User-Id，这两个需要用户自行提供）。
// 返回的 map 可以直接用 JSON 序列化后放入 customHeadersJSON 配置。
//
// 使用方式（config.yaml）：
//
//	customHeadersJSON: '{"Authorization":"Bearer <token>","X-User-Id":"<uuid>","Accept":"application/json",...}'
//
// 或结合 CodeBuddyHeadersJSON() 手动拼上 Auth 和 X-User-Id。
func CodeBuddyStandardHeaders() map[string]string {
	return map[string]string{
		"Accept":                       "application/json",
		"Content-Type":                 "application/json",
		"x-requested-with":             "XMLHttpRequest",
		"User-Agent":                   CodeBuddyUserAgent,
		"X-IDE-Type":                   "CLI",
		"X-IDE-Name":                   "CLI",
		"X-IDE-Version":                CodeBuddyCLIVersion,
		"X-Product":                    "SaaS",
		"X-Agent-Intent":               CodeBuddyAgentIntent,
		"X-Private-Data":               "false",
		"X-Enterprise-Id":              CodeBuddyDefaultEnterpriseID,
		"X-Tenant-Id":                  CodeBuddyDefaultEnterpriseID,
		"X-Domain":                     CodeBuddyDefaultDomain,
		"x-stainless-arch":             "arm64",
		"x-stainless-lang":             "js",
		"x-stainless-os":               "MacOS",
		"x-stainless-package-version":  CodeBuddyStainlessVersion,
		"x-stainless-runtime":          CodeBuddyStainlessRuntime,
		"x-stainless-runtime-version":  CodeBuddyNodeVersion,
		"x-stainless-retry-count":      "0",
	}
}

// CodeBuddyHeadersJSON 返回 CodeBuddyStandardHeaders 的 JSON 字符串。
// 注意：不含 Authorization 和 X-User-Id，需要调用方自行拼接在 config.yaml 的 customHeadersJSON 中。
func CodeBuddyHeadersJSON() string {
	payload, _ := json.Marshal(CodeBuddyStandardHeaders())
	return string(payload)
}

// CodeBuddyFullHeaders 返回包含所有必需 header 的 map（含 X-User-Id，不含 Authorization）。
// xUserID 是从 CodeBuddy CLI 登录后获取的持久用户标识（UUID 格式，如 2f6ed114-...）。
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
// 调用者需自行设置 Authorization 和 X-User-Id。
func ApplyCodeBuddyHeaders(httpReq *http.Request) {
	for key, value := range CodeBuddyStandardHeaders() {
		httpReq.Header.Set(key, value)
	}
}

// CodeBuddyExtraParams 返回 CodeBuddy 需要的额外请求体参数。
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
	// 自动合并 CodeBuddy 标准请求头与用户自定义请求头。
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
	if mergedBytes, err := json.Marshal(mergedHeaders); err == nil {
		req.CustomHeadersJSON = string(mergedBytes)
	}
	req.CustomHeadersEnabled = true

	// 自动合并 CodeBuddy 额外请求体参数。
	if bytes, err := json.Marshal(CodeBuddyExtraParams()); err == nil {
		extraJSON := string(bytes)
		if req.OpenAIExtraParamsEnabled && strings.TrimSpace(req.OpenAIExtraParamsJSON) != "" {
			var base map[string]any
			if json.Unmarshal([]byte(req.OpenAIExtraParamsJSON), &base) == nil {
				base["reasoning_summary"] = "auto"
				if merged, err := json.Marshal(base); err == nil {
					extraJSON = string(merged)
				}
			}
		}
		req.OpenAIExtraParamsJSON = extraJSON
	}
	req.OpenAIExtraParamsEnabled = true

	return a.openai.Stream(ctx, req, sink)
}

// CodeBuddyModelInfo 表示从 /v3/config 返回的模型信息。
type CodeBuddyModelInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MaxInputTokens  int    `json:"maxInputTokens"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	SupportsReasoning bool `json:"supportsReasoning"`
}

// CodeBuddyConfigResponse 表示 /v3/config 的响应结构。
type CodeBuddyConfigResponse struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data CodeBuddyConfigData    `json:"data"`
}

// CodeBuddyConfigData 包含 models 列表。
type CodeBuddyConfigData struct {
	Models []CodeBuddyModelInfo `json:"models"`
}

// CodeBuddyModelDiscovery 用于从 copilot.tencent.com/v3/config 获取可用模型列表。
//
// 代理解析策略（与 CodeBuddyAdapter/Stream 路径保持一致）：
//   - proxyURL 非空（用户在 yaml 中显式配置了代理）→ 强制走 yaml 的代理，绕过 netproxy
//   - proxyURL 为空 → 回退到 netproxy（env / macOS 系统代理 / Proxyman 助手）
type CodeBuddyModelDiscovery struct {
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

// discoveryClientForProxy 复刻 adapterClientForRequest 的代理策略。
// proxyURL 非空时返回仅使用 yaml 代理的 client，否则返回走 netproxy 解析的 client。
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
//
// proxyURL 与 yaml 中 ModelAdapterConfig.Proxy 对齐：非空时优先走 yaml 代理，
// 留空时回退到 netproxy 解析（覆盖 macOS 系统代理 / Proxyman 助手等）。
func (d *CodeBuddyModelDiscovery) FetchModels(ctx context.Context, apiKey string, xUserID string, proxyURL string) ([]CodeBuddyModelInfo, error) {
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