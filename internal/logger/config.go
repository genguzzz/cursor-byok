package logger

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"cursor/internal/appdata"

	"gopkg.in/yaml.v3"
)

// readDebugLogFromConfigFile 读取 config.yaml 的 log 字段。
// 文件不存在或未配置时默认 false，与 menubar 行为一致。
func readDebugLogFromConfigFile() (bool, error) {
	path := appdata.ConfigFilePath()
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("读取 config.yaml 失败: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(payload, &root); err != nil {
		return false, fmt.Errorf("解析 config.yaml 失败: %w", err)
	}
	return boolConfigValue(&root, "log"), nil
}

func boolConfigValue(root *yaml.Node, key string) bool {
	node := findChildNode(rootMapping(root), key)
	if node == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(node.Value)) {
	case "true", "yes", "1", "on":
		return true
	default:
		if node.Tag == "!!bool" {
			v, err := strconv.ParseBool(node.Value)
			return err == nil && v
		}
		return false
	}
}

func rootMapping(root *yaml.Node) *yaml.Node {
	if root == nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

func findChildNode(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}