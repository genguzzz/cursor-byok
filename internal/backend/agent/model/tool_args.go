package modeladapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// completedToolArgsJSON 校验并规整工具参数 JSON。
// OpenAI / Anthropic 的 function arguments 必须是 JSON object；流中断截断时常见非法 JSON，
// 若原样发出会写入 history，后续 replay 会被 provider 以 400 invalid_parameter_value 拒绝。
func completedToolArgsJSON(toolName string, arguments string) ([]byte, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return []byte("{}"), nil
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		name := strings.TrimSpace(toolName)
		if name == "" {
			name = "tool"
		}
		return nil, fmt.Errorf("incomplete or malformed tool input for %s: %w", name, err)
	}
	if value == nil {
		name := strings.TrimSpace(toolName)
		if name == "" {
			name = "tool"
		}
		return nil, fmt.Errorf("non-object tool input for %s", name)
	}
	return []byte(trimmed), nil
}

func isValidProviderToolArgumentsJSON(arguments string) bool {
	_, err := completedToolArgsJSON("", arguments)
	return err == nil
}
