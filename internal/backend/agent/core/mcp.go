package runtimecore

import (
	"encoding/json"
	"strings"
)

// MCPToolPayload 表示 CallMcpTool 的宽容解码结果。
type MCPToolPayload struct {
	Server             string
	ProviderIdentifier string
	ToolName           string
	Name               string
	Arguments          map[string]any
}

// DecodeMCPToolPayload 解析 CallMcpTool 参数，并兼容字符串化的 arguments 对象。
//
// 模型可能混用 snake_case、嵌套形态，或把整包塞进 args，
// 导致 lookup name 为空、客户端回 tool not found: ?。这里按官方 camelCase
// 为主，同时接受 snake_case 与嵌套形态。
func DecodeMCPToolPayload(raw []byte) (MCPToolPayload, error) {
	payload := MCPToolPayload{
		Arguments: make(map[string]any),
	}
	if len(raw) == 0 {
		return payload, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return payload, err
	}
	if decoded == nil {
		return payload, nil
	}

	payload.Server = firstDecodedString(decoded, "server")
	payload.ProviderIdentifier = firstDecodedString(decoded, "providerIdentifier", "provider_identifier")
	payload.ToolName = firstDecodedString(decoded, "toolName", "tool_name")
	payload.Name = firstDecodedString(decoded, "name")
	payload.Arguments = decodeJSONObjectLike(decoded["arguments"])
	if len(payload.Arguments) == 0 {
		payload.Arguments = decodeJSONObjectLike(decoded["args"])
	}
	if len(payload.Arguments) == 0 {
		payload.Arguments = decodeJSONObjectLike(decoded["tool_args"])
	}
	liftMCPToolPayloadFromNestedArguments(&payload)
	if strings.TrimSpace(payload.Server) == "" {
		payload.Server = payload.ProviderIdentifier
	}
	payload.Server = NormalizeMCPServerIdentifier(payload.Server)
	payload.ProviderIdentifier = NormalizeMCPServerIdentifier(payload.ProviderIdentifier)
	return payload, nil
}

func liftMCPToolPayloadFromNestedArguments(payload *MCPToolPayload) {
	if payload == nil || len(payload.Arguments) == 0 {
		return
	}
	args := payload.Arguments
	liftedIdentity := false
	if strings.TrimSpace(payload.Server) == "" {
		if value := firstDecodedString(args, "server", "providerIdentifier", "provider_identifier"); value != "" {
			payload.Server = value
			liftedIdentity = true
		}
	}
	if strings.TrimSpace(payload.ProviderIdentifier) == "" {
		if value := firstDecodedString(args, "providerIdentifier", "provider_identifier"); value != "" {
			payload.ProviderIdentifier = value
			liftedIdentity = true
		}
	}
	if strings.TrimSpace(payload.ToolName) == "" {
		if value := firstDecodedString(args, "toolName", "tool_name"); value != "" {
			payload.ToolName = value
			liftedIdentity = true
		}
	}
	if strings.TrimSpace(payload.Name) == "" {
		if value := firstDecodedString(args, "name"); value != "" {
			payload.Name = value
			liftedIdentity = true
		}
	}
	if !liftedIdentity {
		return
	}
	if nested := decodeJSONObjectLike(args["tool_args"]); len(nested) > 0 {
		payload.Arguments = nested
		return
	}
	cleaned := make(map[string]any, len(args))
	for key, value := range args {
		switch strings.TrimSpace(key) {
		case "server", "providerIdentifier", "provider_identifier", "toolName", "tool_name", "name", "tool_args":
			continue
		default:
			cleaned[key] = value
		}
	}
	payload.Arguments = cleaned
}

// NormalizeMCPServerIdentifier 把模型漏写的 user- 前缀补上。
//
// 客户端注册名形如 user-xxx / cursor-xxx；漏写前缀会导致 lookup name 查无此工具。
func NormalizeMCPServerIdentifier(server string) string {
	trimmed := strings.TrimSpace(server)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "user-") || strings.HasPrefix(trimmed, "cursor-") {
		return trimmed
	}
	return "user-" + trimmed
}

// InferMCPServerIdentifier 从 canonical lookup name 中反推出 server identifier。
//
// 注册名形如 user-<server>-<tool>；工具名通常不含 '-'。
// 按最后一个 '-' 切开，避免把 user-<server> 误切成 user。
func InferMCPServerIdentifier(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	index := strings.LastIndex(trimmed, "-")
	if index <= 0 {
		return ""
	}
	return NormalizeMCPServerIdentifier(strings.TrimSpace(trimmed[:index]))
}

// InferMCPToolName 从 canonical lookup name 中反推出 tool name。
func InferMCPToolName(serverIdentifier string, name string) string {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return ""
	}
	trimmedServer := strings.TrimSpace(serverIdentifier)
	if trimmedServer != "" && strings.HasPrefix(trimmedName, trimmedServer+"-") {
		return strings.TrimSpace(strings.TrimPrefix(trimmedName, trimmedServer+"-"))
	}
	if index := strings.LastIndex(trimmedName, "-"); index > 0 && index+1 < len(trimmedName) {
		return strings.TrimSpace(trimmedName[index+1:])
	}
	return trimmedName
}

// ResolveMCPToolInvocation 从解码结果得到客户端 lookup 用的 server / toolName / args。
func ResolveMCPToolInvocation(payload MCPToolPayload) (server string, toolName string, args map[string]any) {
	server = strings.TrimSpace(payload.Server)
	if server == "" {
		server = strings.TrimSpace(payload.ProviderIdentifier)
	}
	toolName = strings.TrimSpace(payload.ToolName)
	if toolName == "" {
		toolName = InferMCPToolName(server, payload.Name)
	}
	if server == "" && strings.TrimSpace(payload.Name) != "" {
		server = InferMCPServerIdentifier(payload.Name)
		if toolName == "" {
			toolName = InferMCPToolName(server, payload.Name)
		}
	}
	server = NormalizeMCPServerIdentifier(server)
	args = payload.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return server, toolName, args
}

func firstDecodedString(values map[string]any, keys ...string) string {
	if values == nil {
		return ""
	}
	for _, key := range keys {
		if text := decodeJSONStringValue(values[key]); text != "" {
			return text
		}
	}
	return ""
}

func decodeJSONStringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func decodeJSONObjectLike(value any) map[string]any {
	switch item := value.(type) {
	case map[string]any:
		if item == nil {
			return make(map[string]any)
		}
		return item
	case string:
		return decodeJSONObjectBytes([]byte(item))
	case []byte:
		return decodeJSONObjectBytes(item)
	case json.RawMessage:
		return decodeJSONObjectBytes([]byte(item))
	default:
		return make(map[string]any)
	}
}

func decodeJSONObjectBytes(raw []byte) map[string]any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return make(map[string]any)
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return make(map[string]any)
	}
	object, ok := decoded.(map[string]any)
	if !ok || object == nil {
		return make(map[string]any)
	}
	return object
}
