// codebuddy.go 提供 CodeBuddy CLI 兼容的请求头和请求体辅助函数。
//
// CodeBuddy CLI (v2.125.5) 通过 copilot.tencent.com 的 OpenAI 兼容端点
// 使用 DeepSeek 等模型，请求需要携带特定的认证、追踪和兼容性头。
// 本文件将这些头定义为一组可复用的常量与函数，供适配器内部使用，
// 也可在 config.yaml 中通过 customHeadersJSON 引用。
package modeladapter

import (
	"encoding/json"
	"net/http"
)

// CodeBuddy CLI 客户端常量 —— 与 v2.125.5 版本对齐。
const (
	CodeBuddyCLIVersion          = "2.125.5"
	CodeBuddyStainlessVersion    = "6.25.0"
	CodeBuddyStainlessRuntime    = "node"
	CodeBuddyUserAgent           = "CLI/2.125.5 CodeBuddy/2.125.5"
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
		"x-codebuddy-request":          "1",
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
		"x-stainless-runtime-version":  "v22.12.0",
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