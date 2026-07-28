// tab_renamer.go 实现 Cursor 客户端的 NameTab / NameAgent RPC 处理。
//
// 目标：让 Cursor 在新建会话后能拿到 AI 生成的短标题，而不是退化成"用第一条消息"。
// 实现走 service.provider.StartStream 发起一次轻量同步补全，
// 在 sink 里只收集 text delta，最终用第一行作为标题回给客户端。
//
// 配置走 yaml 的 features.tabRenamer：默认 disabled，禁用时 NameTab/NameAgent
// 返回空 name + 200 OK，让 Cursor 客户端走自身降级逻辑，避免 404 破坏。
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	serverconfig "cursor/internal/backend/server/config"
	modeladapter "cursor/internal/backend/agent/model"
)

const (
	tabRenamerSystemPrompt = "你是一个会话标题生成器。根据用户与助手的第一轮对话，输出一行简短的中文标题，" +
		"长度不超过 40 个字符。只输出标题本身，不要任何前缀、解释、引号、换行或 Markdown 标记。"

	tabRenamerDefaultMaxNameChars    = 50
	tabRenamerDefaultMaxOutputTokens = 64
	tabRenamerDefaultTimeoutSeconds  = 8
	tabRenamerDefaultMaxInputChars   = 4000

	tabRenamerRequestIDPrefix = "tab-renamer-"
)

// errTabRenamerNoProvider 表示 provider 网关未初始化，不应视作硬错误（直接返回空 name）。
var errTabRenamerNoProvider = errors.New("tab renamer provider is not initialized")

// TabRenamerConfigLoader 由 Module 在装配时注入，把 Service 和 yaml 配置解耦。
// 返回零值时等价于"未启用"，NameTab/NameAgent 会直接返回空 name。
type TabRenamerConfigLoader func() serverconfig.TabRenamerConfig

// NameTab 处理 aiserver.v1.AiService.NameTab RPC。
func (service *Service) NameTab(ctx context.Context, req *connect.Request[aiserverv1.NameTabRequest]) (*connect.Response[aiserverv1.NameTabResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&aiserverv1.NameTabResponse{}), nil
	}
	cfg := service.resolveTabRenamerConfig()
	if !cfg.Enabled {
		return connect.NewResponse(&aiserverv1.NameTabResponse{}), nil
	}
	prompt := flattenNameTabConversationMessages(req.Msg.GetMessages(), cfg.MaxInputChars)
	if prompt == "" {
		return connect.NewResponse(&aiserverv1.NameTabResponse{}), nil
	}
	name, err := service.generateTabName(ctx, cfg, tabRenamerRequestIDPrefix+uuid.NewString(), tabRenamerSystemPrompt, prompt)
	if err != nil {
		log.Printf("forwarder NameTab generation failed error=%v", err)
		return connect.NewResponse(&aiserverv1.NameTabResponse{}), nil
	}
	if name == "" {
		return connect.NewResponse(&aiserverv1.NameTabResponse{}), nil
	}
	return connect.NewResponse(&aiserverv1.NameTabResponse{Name: name}), nil
}

// NameAgent 处理 aiserver.v1.AiService.NameAgent RPC。
func (service *Service) NameAgent(ctx context.Context, req *connect.Request[aiserverv1.NameAgentRequest]) (*connect.Response[aiserverv1.NameAgentResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&aiserverv1.NameAgentResponse{}), nil
	}
	cfg := service.resolveTabRenamerConfig()
	if !cfg.Enabled {
		return connect.NewResponse(&aiserverv1.NameAgentResponse{}), nil
	}
	userMessage := strings.TrimSpace(req.Msg.GetUserMessage())
	if userMessage == "" {
		return connect.NewResponse(&aiserverv1.NameAgentResponse{}), nil
	}
	prompt := truncateRunes(userMessage, cfg.MaxInputChars)
	if prompt == "" {
		return connect.NewResponse(&aiserverv1.NameAgentResponse{}), nil
	}
	name, err := service.generateTabName(ctx, cfg, tabRenamerRequestIDPrefix+uuid.NewString(), tabRenamerSystemPrompt, prompt)
	if err != nil {
		log.Printf("forwarder NameAgent generation failed error=%v", err)
		return connect.NewResponse(&aiserverv1.NameAgentResponse{}), nil
	}
	if name == "" {
		return connect.NewResponse(&aiserverv1.NameAgentResponse{}), nil
	}
	return connect.NewResponse(&aiserverv1.NameAgentResponse{Name: name}), nil
}

// resolveTabRenamerConfig 返回当前会话使用的 TabRenamer 配置。
// 未注入 loader 或 loader 失败时退化为禁用状态。
func (service *Service) resolveTabRenamerConfig() serverconfig.TabRenamerConfig {
	if service == nil || service.tabRenamerConfigLoader == nil {
		return serverconfig.TabRenamerConfig{}
	}
	load := service.tabRenamerConfigLoader
	if load == nil {
		return serverconfig.TabRenamerConfig{}
	}
	cfg := load()
	cfg.MaxInputChars = normalizeNonNegativeTabRuneCount(cfg.MaxInputChars, tabRenamerDefaultMaxInputChars)
	cfg.MaxOutputTokens = normalizeNonNegativeTabRuneCount(cfg.MaxOutputTokens, tabRenamerDefaultMaxOutputTokens)
	cfg.MaxNameChars = normalizeNonNegativeTabRuneCount(cfg.MaxNameChars, tabRenamerDefaultMaxNameChars)
	cfg.TimeoutSeconds = normalizeNonNegativeTabRuneCount(cfg.TimeoutSeconds, tabRenamerDefaultTimeoutSeconds)
	return cfg
}

func normalizeNonNegativeTabRuneCount(value int, fallback int) int {
	if value < 0 {
		return 0
	}
	if value == 0 {
		return fallback
	}
	return value
}

// generateTabName 调一次轻量同步补全，返回清洗后的单行短标题。
func (service *Service) generateTabName(parent context.Context, cfg serverconfig.TabRenamerConfig, requestID string, systemPrompt string, userContent string) (string, error) {
	if service.provider == nil {
		return "", errTabRenamerNoProvider
	}
	if service.resolver == nil {
		return "", fmt.Errorf("tab renamer channel resolver is not initialized")
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = tabRenamerDefaultTimeoutSeconds * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	modelID, modelSource, err := service.resolveTabRenamerModelID(ctx, cfg.ModelID)
	if err != nil || strings.TrimSpace(modelID) == "" {
		return "", fmt.Errorf("resolve tab renamer model: %w", err)
	}
	maxOutput := cfg.MaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = tabRenamerDefaultMaxOutputTokens
	}
	textAccumulated := ""
	sink := func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			textAccumulated += event.Text
			return nil
		case modeladapter.ModelEventKindThinkingDelta,
			modeladapter.ModelEventKindThinkingCompleted,
			modeladapter.ModelEventKindTurnFinished:
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted,
			modeladapter.ModelEventKindPartialToolCall,
			modeladapter.ModelEventKindToolCallDelta:
			return fmt.Errorf("tab renamer must not invoke tools")
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return providerTerminalError{cause: event.Err}
			}
			return providerTerminalError{cause: fmt.Errorf("provider error")}
		default:
			return nil
		}
	}
	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    requestID + "-model",
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		ThinkingEffort: "disabled",
		Messages:       []modeladapter.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userContent}},
		Tools:          nil,
		MaxTokens:      maxOutput,
		CompileSummary: fmt.Sprintf("tab_renamer source=%s", modelSource),
	}, sink)
	if err != nil {
		return "", err
	}
	rawOutput := strings.TrimSpace(textAccumulated)
	name := cleanGeneratedTabName(rawOutput, cfg.MaxNameChars)
	if name == "" {
		log.Printf("forwarder tab_renamer cleaned to empty text=%q", strings.TrimSpace(textAccumulated))
		return "", fmt.Errorf("tab renamer returned empty name")
	}
	return name, nil
}

// resolveTabRenamerModelID 优先使用用户配置指定 modelID；否则取上次 Agent 用的；
// 都不可用时报错让上层走静默空名降级。
func (service *Service) resolveTabRenamerModelID(ctx context.Context, configured string) (string, string, error) {
	explicit := strings.TrimSpace(configured)
	if explicit != "" {
		if channelID := service.lookupTabRenamerChannelIDByModelField(explicit); channelID != "" {
			return channelID, "configured", nil
		}
		if service.resolver != nil {
			channel, err := service.resolver.SelectChannelForModel(ctx, explicit)
			if err == nil && channel != nil && strings.TrimSpace(channel.ID) != "" {
				return strings.TrimSpace(channel.ID), "configured_id", nil
			}
		}
	}
	if service.modelMemory != nil && service.resolver != nil {
		hash := strings.TrimSpace(service.modelMemory.LastAgentModelHash())
		if hash != "" {
			channel, err := service.resolver.SelectChannelForModel(ctx, hash)
			if err == nil && channel != nil && strings.TrimSpace(channel.ID) == hash {
				return hash, "last_agent_model_hash", nil
			}
		}
	}
	return "", "default_fallback", fmt.Errorf("no available channel for tab renamer")
}

// lookupTabRenamerChannelIDByModelField 在 service.tabRenamerFullConfigLoader 返回的全量 config 里
// 按 modelID 字段匹配 adapter，绕过 resolver 内部按 channel.ID 索引的限制。
// 找到则返回其归一化后的 channel.ID。
func (service *Service) lookupTabRenamerChannelIDByModelField(target string) string {
	if service == nil || service.tabRenamerFullConfigLoader == nil {
		return ""
	}
	load := service.tabRenamerFullConfigLoader
	if load == nil {
		return ""
	}
	cfg := load()
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	for _, adapter := range cfg.ModelAdapters {
		if strings.TrimSpace(adapter.ModelID) != target && strings.TrimSpace(adapter.ProviderModelID) != target {
			continue
		}
		if strings.TrimSpace(adapter.ID) == "" {
			normalized, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{adapter})
			if err != nil || len(normalized) == 0 {
				continue
			}
			return strings.TrimSpace(normalized[0].ID)
		}
		return strings.TrimSpace(adapter.ID)
	}
	return ""
}

// flattenNameTabConversationMessages 把客户端传入的对话历史压成一段纯文本。
// 超过 maxChars 时只保留尾部 N 字符，保证 prompt 不会撑爆。
func flattenNameTabConversationMessages(messages []*aiserverv1.ConversationMessage, maxChars int) string {
	if len(messages) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = tabRenamerDefaultMaxInputChars
	}
	var builder strings.Builder
	for _, message := range messages {
		if message == nil {
			continue
		}
		text := extractNameTabMessageText(message)
		if text == "" {
			continue
		}
		role := nameTabRoleLabel(message.GetType())
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		fmt.Fprintf(&builder, "%s: %s", role, text)
	}
	return truncateRunes(strings.TrimSpace(builder.String()), maxChars)
}

// nameTabRoleLabel 把 ConversationMessage 的 type 枚举翻译成中文 role 标签。
func nameTabRoleLabel(messageType aiserverv1.ConversationMessage_MessageType) string {
	switch messageType {
	case aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN:
		return "user"
	case aiserverv1.ConversationMessage_MESSAGE_TYPE_AI:
		return "assistant"
	default:
		return "user"
	}
}

// extractNameTabMessageText 从单个 ConversationMessage 里抠出主文本。
func extractNameTabMessageText(message *aiserverv1.ConversationMessage) string {
	if message == nil {
		return ""
	}
	if text := strings.TrimSpace(message.GetText()); text != "" {
		return text
	}
	if text := strings.TrimSpace(message.GetRichText()); text != "" {
		return text
	}
	return ""
}

func truncateRunes(value string, maxChars int) string {
	if maxChars <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value
	}
	return string(runes[len(runes)-maxChars:])
}

// cleanGeneratedTabName 把模型原始输出清洗成单行短标题。
func cleanGeneratedTabName(raw string, maxChars int) string {
	result := strings.TrimSpace(raw)
	result = stripTabNameCodeFence(result)
	prefixes := []string{
		"会话标题：", "会话标题:", "标题：", "标题:",
		"title:", "title：",
	}
	for {
		lower := strings.ToLower(strings.TrimSpace(result))
		matched := ""
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, prefix) {
				matched = prefix
				break
			}
		}
		if matched == "" {
			break
		}
		result = strings.TrimSpace(result[len(matched):])
	}
	if idx := strings.IndexAny(result, "\n\r"); idx >= 0 {
		result = result[:idx]
	}
	result = strings.TrimSpace(result)
	result = strings.Trim(result, "\"'`“”‘’")
	if maxChars <= 0 {
		maxChars = tabRenamerDefaultMaxNameChars
	}
	runes := []rune(result)
	if len(runes) > maxChars {
		runes = runes[:maxChars]
	}
	return strings.TrimSpace(string(runes))
}

func stripTabNameCodeFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}