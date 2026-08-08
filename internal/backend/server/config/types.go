package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"cursor/internal/modelchannel"
)

const (
	DefaultBackendListenAddr                = "127.0.0.1:18090"
	DefaultProxyListenAddr                  = "127.0.0.1:18080"
	DefaultFrontendBaseURL                  = "http://127.0.0.1"
	DefaultProviderStreamIdleTimeoutSeconds = 240
	MinProviderStreamIdleTimeoutSeconds     = 30
)

type ModelAdapterConfig struct {
	ID                          string `json:"id,omitempty" yaml:"-"`
	Sort                        int    `json:"sort" yaml:"sort"`
	DisplayName                 string `json:"displayName" yaml:"displayName"`
	Type                        string `json:"type" yaml:"type"`
	BaseURL                     string `json:"baseURL" yaml:"baseURL"`
	APIKey                      string `json:"apiKey" yaml:"apiKey"`
	Proxy                       string `json:"proxy,omitempty" yaml:"proxy,omitempty"`
	TooltipData                 string `json:"tooltipData" yaml:"tooltipData"`
	ModelID                     string `json:"modelID" yaml:"modelID"`
	ProviderModelID             string `json:"providerModelID,omitempty" yaml:"providerModelID,omitempty"`
	ReasoningEffort             string `json:"reasoningEffort" yaml:"reasoningEffort"`
	OpenAIEndpoint              string `json:"openAIEndpoint" yaml:"openAIEndpoint"`
	OpenAIExtraParamsEnabled    bool   `json:"openAIExtraParamsEnabled" yaml:"openAIExtraParamsEnabled"`
	OpenAIExtraParamsJSON       string `json:"openAIExtraParamsJSON" yaml:"openAIExtraParamsJSON"`
	CustomHeadersEnabled        bool   `json:"customHeadersEnabled" yaml:"customHeadersEnabled"`
	CustomHeadersJSON           string `json:"customHeadersJSON" yaml:"customHeadersJSON"`
	AnthropicExtraParamsEnabled bool   `json:"anthropicExtraParamsEnabled" yaml:"anthropicExtraParamsEnabled"`
	AnthropicExtraParamsJSON    string `json:"anthropicExtraParamsJSON" yaml:"anthropicExtraParamsJSON"`
	ContextWindowTokens         int    `json:"contextWindowTokens" yaml:"contextWindowTokens"`
	MaxCompletionTokens         int    `json:"maxCompletionTokens" yaml:"maxCompletionTokens"`
	AnthropicMaxTokens          int    `json:"anthropicMaxTokens" yaml:"anthropicMaxTokens"`
	AnthropicThinkingEffort     string `json:"anthropicThinkingEffort,omitempty" yaml:"anthropicThinkingEffort,omitempty"`
	ThinkingBudgetTokens        int    `json:"thinkingBudgetTokens" yaml:"thinkingBudgetTokens"`
}

type HomeMetricsConfig struct {
	IncludeCacheWriteInHitRate bool `json:"includeCacheWriteInHitRate" yaml:"includeCacheWriteInHitRate"`
}

// TabRenamerConfig 控制是否在本地模式接管 Cursor 的 NameTab / NameAgent RPC，
// 用一个轻量模型把会话首轮消息压缩成单行短标题。
//
// opt-in 特性：默认 disabled，避免在没看到该配置的用户机器上默默多打一次模型。
//
// 路由语义：
//   - mixed 关闭：请求始终进本地 handler；disabled 时返回空 name + 200 OK，
//     Cursor 客户端降级到"用第一条消息"作为标题。
//   - mixed 开启：enabled 时走本地生成；disabled 时 AIServiceCatchAll 回源官方命名服务，
//     不再拦截为空标题。
type TabRenamerConfig struct {
	// Enabled 是否由本地接管 NameTab / NameAgent。
	// mixed 开启且为 false 时，这两条 RPC 回源官方。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// ModelID 可选：指定一个模型 ID（用户配置在 modelAdapters 里的）。
	// 为空时退到"上次 Agent 使用的模型"，再退到 default_fallback（报错后上层空名降级）。
	ModelID string `json:"modelID,omitempty" yaml:"modelID,omitempty"`
	// MaxInputChars 输入侧拼接消息时的最大字符数；超过会被截断到末尾 N 字符。
	MaxInputChars int `json:"maxInputChars,omitempty" yaml:"maxInputChars,omitempty"`
	// MaxOutputTokens 单次生成的 max_tokens 上限。
	MaxOutputTokens int `json:"maxOutputTokens,omitempty" yaml:"maxOutputTokens,omitempty"`
	// MaxNameChars 标题字符数上限，超过会被截断。
	MaxNameChars int `json:"maxNameChars,omitempty" yaml:"maxNameChars,omitempty"`
	// TimeoutSeconds 同步等待模型返回的超时（秒），0 表示使用默认 8 秒。
	TimeoutSeconds int `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
}

// MixedModelRoutingConfig 控制桌面端官方模型与注入模型混用。
// 缺省开启：保留真实 Cursor 登录，模型列表合并，Agent 按渠道分流。
type MixedModelRoutingConfig struct {
	// Enabled 为 nil 或未配置时视为 true。显式 false 回到旧的全本地 mock。
	Enabled *bool `json:"enabled" yaml:"enabled"`
}

// IsEnabled 返回混合分流是否开启；未配置时默认 true。
func (cfg MixedModelRoutingConfig) IsEnabled() bool {
	if cfg.Enabled == nil {
		return true
	}
	return *cfg.Enabled
}

// FeaturesConfig 收纳所有 opt-in / 灰度特性的配置。
type FeaturesConfig struct {
	TabRenamer        TabRenamerConfig        `json:"tabRenamer" yaml:"tabRenamer"`
	MixedModelRouting MixedModelRoutingConfig `json:"mixedModelRouting" yaml:"mixedModelRouting"`
}

type Config struct {
	Log                       bool                 `json:"log" yaml:"log"`
	ProviderStreamIdleTimeout int                  `json:"providerStreamIdleTimeout" yaml:"providerStreamIdleTimeout"`
	BackendListenAddr         string               `json:"backendListenAddr" yaml:"backendListenAddr"`
	ProxyListenAddr           string               `json:"proxyListenAddr" yaml:"proxyListenAddr"`
	ModelAdapters             []ModelAdapterConfig `json:"modelAdapters" yaml:"modelAdapters"`
	HomeMetrics               HomeMetricsConfig    `json:"homeMetrics" yaml:"homeMetrics"`
	LastAgentModelHash        string               `json:"lastAgentModelHash" yaml:"lastAgentModelHash"`
	Features                  FeaturesConfig       `json:"features" yaml:"features"`
}

func DefaultConfig() Config {
	return Config{
		Log:                       false,
		ProviderStreamIdleTimeout: DefaultProviderStreamIdleTimeoutSeconds,
		BackendListenAddr:         DefaultBackendListenAddr,
		ProxyListenAddr:           DefaultProxyListenAddr,
		ModelAdapters:             []ModelAdapterConfig{},
		Features: FeaturesConfig{
			MixedModelRouting: MixedModelRoutingConfig{Enabled: boolPtr(true)},
		},
	}
}

func NormalizeConfig(input Config) (Config, error) {
	output := DefaultConfig()
	output.Log = input.Log
	output.ProviderStreamIdleTimeout = normalizeProviderStreamIdleTimeout(input.ProviderStreamIdleTimeout)
	backendListenAddr, err := normalizeListenAddr(input.BackendListenAddr, DefaultBackendListenAddr, "backendListenAddr")
	if err != nil {
		return Config{}, err
	}
	proxyListenAddr, err := normalizeListenAddr(input.ProxyListenAddr, DefaultProxyListenAddr, "proxyListenAddr")
	if err != nil {
		return Config{}, err
	}
	output.BackendListenAddr = backendListenAddr
	output.ProxyListenAddr = proxyListenAddr
	output.HomeMetrics.IncludeCacheWriteInHitRate = input.HomeMetrics.IncludeCacheWriteInHitRate
	output.LastAgentModelHash = strings.TrimSpace(input.LastAgentModelHash)
	output.Features.TabRenamer = normalizeTabRenamerConfig(input.Features.TabRenamer)
	output.Features.MixedModelRouting = normalizeMixedModelRoutingConfig(input.Features.MixedModelRouting)
	adapters, err := NormalizeModelAdapterConfigs(input.ModelAdapters)
	if err != nil {
		return Config{}, err
	}
	output.ModelAdapters = adapters
	return output, nil
}

func normalizeTabRenamerConfig(input TabRenamerConfig) TabRenamerConfig {
	return TabRenamerConfig{
		Enabled:         input.Enabled,
		ModelID:         strings.TrimSpace(input.ModelID),
		MaxInputChars:   normalizeNonNegative(input.MaxInputChars, 4000),
		MaxOutputTokens: normalizeNonNegative(input.MaxOutputTokens, 64),
		MaxNameChars:    normalizeNonNegative(input.MaxNameChars, 50),
		TimeoutSeconds:  normalizeNonNegative(input.TimeoutSeconds, 8),
	}
}

func normalizeMixedModelRoutingConfig(input MixedModelRoutingConfig) MixedModelRoutingConfig {
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return MixedModelRoutingConfig{Enabled: &enabled}
}

func boolPtr(value bool) *bool {
	return &value
}

func normalizeNonNegative(value int, fallback int) int {
	if value < 0 {
		return 0
	}
	if value == 0 {
		return fallback
	}
	return value
}

func NormalizeModelAdapterConfigs(input []ModelAdapterConfig) ([]ModelAdapterConfig, error) {
	if len(input) == 0 {
		return []ModelAdapterConfig{}, nil
	}

	normalized := make([]ModelAdapterConfig, 0, len(input))
	seenChannelIDs := make(map[string]struct{}, len(input))
	for _, item := range input {
		baseURL, err := modelchannel.NormalizeBaseURL(item.BaseURL)
		if err != nil {
			return nil, err
		}
		nextType := normalizeModelAdapterType(item.Type)
		next := ModelAdapterConfig{
			Sort:                 item.Sort,
			DisplayName:          strings.TrimSpace(item.DisplayName),
			Type:                 nextType,
			BaseURL:              baseURL,
			APIKey:               strings.TrimSpace(item.APIKey),
			Proxy:                strings.TrimSpace(item.Proxy),
			TooltipData:          strings.TrimSpace(item.TooltipData),
			ModelID:              strings.TrimSpace(item.ModelID),
			ProviderModelID:      strings.TrimSpace(item.ProviderModelID),
			ReasoningEffort:      normalizeReasoningEffort(item.ReasoningEffort),
			OpenAIEndpoint:       modelchannel.NormalizeOpenAIEndpoint(item.Type, item.OpenAIEndpoint),
			ContextWindowTokens:  normalizeMaxCompletionTokens(item.ContextWindowTokens),
			MaxCompletionTokens:  normalizeMaxCompletionTokens(item.MaxCompletionTokens),
			AnthropicMaxTokens:   normalizeMaxCompletionTokens(item.AnthropicMaxTokens),
			ThinkingBudgetTokens: normalizeMaxCompletionTokens(item.ThinkingBudgetTokens),
		}
		if next.Type == "openai" || next.Type == "codebuddy" {
			next.OpenAIExtraParamsEnabled = item.OpenAIExtraParamsEnabled
			next.OpenAIExtraParamsJSON = strings.TrimSpace(item.OpenAIExtraParamsJSON)
		} else if next.Type == "anthropic" {
			next.AnthropicThinkingEffort = normalizeAnthropicThinkingEffort(item.AnthropicThinkingEffort)
			next.AnthropicExtraParamsEnabled = item.AnthropicExtraParamsEnabled
			next.AnthropicExtraParamsJSON = strings.TrimSpace(item.AnthropicExtraParamsJSON)
		}
		next.CustomHeadersEnabled = item.CustomHeadersEnabled
		next.CustomHeadersJSON = strings.TrimSpace(item.CustomHeadersJSON)
		switch {
		case next.DisplayName == "":
			return nil, errors.New("模型适配器 displayName 不能为空")
		case next.Type == "":
			return nil, errors.New("模型适配器 type 仅支持 openai、anthropic 或 codebuddy")
		case next.APIKey == "":
			return nil, errors.New("模型适配器 apiKey 不能为空")
		case next.TooltipData == "":
			return nil, errors.New("模型适配器 tooltipData 不能为空")
		case next.ModelID == "":
			return nil, errors.New("模型适配器 modelID 不能为空")
		case (next.Type == "openai" || next.Type == "codebuddy") && next.ReasoningEffort == "":
			return nil, errors.New("模型适配器 reasoningEffort 仅支持 low、medium、high、xhigh、max")
		case (next.Type == "openai" || next.Type == "codebuddy") && next.OpenAIEndpoint == "":
			return nil, errors.New("模型适配器 openAIEndpoint 仅支持 /v1/responses、/v1/chat/completions 或 /custom（自定义路径）")
		case (next.Type == "openai" || next.Type == "codebuddy") && next.OpenAIExtraParamsEnabled:
			if err := validateJSONMap(next.OpenAIExtraParamsJSON, "openAIExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.CustomHeadersEnabled:
			if err := validateHeadersJSON(next.CustomHeadersJSON); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicExtraParamsEnabled:
			if err := validateJSONMap(next.AnthropicExtraParamsJSON, "anthropicExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicThinkingEffort == "":
			return nil, errors.New("模型适配器 anthropicThinkingEffort 仅支持 low、medium、high、xhigh、max")
		}
		next.ID = modelchannel.BuildChannelID(next.BaseURL, next.ModelID, next.APIKey, next.DisplayName, next.OpenAIEndpoint)
		if _, exists := seenChannelIDs[next.ID]; exists {
			return nil, errors.New("模型适配器渠道不能重复，请检查 url、modelID、apiKey、displayName、endpoint 组合")
		}
		seenChannelIDs[next.ID] = struct{}{}
		normalized = append(normalized, next)
	}
	normalizeModelAdapterSorts(normalized)
	return normalized, nil
}

func normalizeModelAdapterSorts(adapters []ModelAdapterConfig) {
	sort.SliceStable(adapters, func(leftIndex, rightIndex int) bool {
		left := adapters[leftIndex].Sort
		right := adapters[rightIndex].Sort
		switch {
		case left <= 0 && right <= 0:
			return false
		case left <= 0:
			return false
		case right <= 0:
			return true
		default:
			return left < right
		}
	})
	for index := range adapters {
		adapters[index].Sort = index + 1
	}
}

func validateJSONMap(value string, fieldName string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return fmt.Errorf("模型适配器 %s 不能为空", fieldName)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return fmt.Errorf("模型适配器 %s 必须是合法 JSON 对象", fieldName)
	}
	if parsed == nil {
		return fmt.Errorf("模型适配器 %s 必须是 JSON 对象", fieldName)
	}
	return nil
}

func validateHeadersJSON(value string) error {
	text := strings.TrimSpace(value)
	if err := validateJSONMap(text, "customHeadersJSON"); err != nil {
		return err
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return errors.New("模型适配器 customHeadersJSON 的值必须是字符串")
	}
	for key := range parsed {
		if strings.TrimSpace(key) == "" {
			return errors.New("模型适配器 customHeadersJSON 的请求头名称不能为空")
		}
	}
	return nil
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "medium":
		return "medium"
	case "low", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeAnthropicThinkingEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xhigh":
		return "xhigh"
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeListenAddr(value string, defaultValue string, fieldName string) (string, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		addr = defaultValue
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s 必须是 host:port 格式", fieldName)
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("%s host 不能为空", fieldName)
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", fmt.Errorf("%s port 必须在 1-65535 之间", fieldName)
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

func normalizeProviderStreamIdleTimeout(value int) int {
	if value <= 0 {
		return DefaultProviderStreamIdleTimeoutSeconds
	}
	if value < MinProviderStreamIdleTimeoutSeconds {
		return MinProviderStreamIdleTimeoutSeconds
	}
	return value
}

func normalizeMaxCompletionTokens(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func normalizeModelAdapterType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	case "codebuddy":
		return "codebuddy"
	default:
		return ""
	}
}
