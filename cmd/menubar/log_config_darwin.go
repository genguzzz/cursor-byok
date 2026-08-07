//go:build darwin

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// readDebugLogEnabledFromFile 读取 config.yaml 的 log 开关；文件不存在或未配置时默认 false。
func readDebugLogEnabledFromFile(path string) (bool, error) {
	root, err := loadConfigRoot(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return boolConfigValue(root, "log"), nil
}

// writeDebugLogEnabledToFile 写入 config.yaml 的 log 字段，保留注释与其它键顺序。
func writeDebugLogEnabledToFile(path string, enable bool) error {
	root, err := loadConfigRoot(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		root = &yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
			}},
		}
	}
	if err := setBoolConfigValue(root, "log", enable); err != nil {
		return err
	}
	return writeConfigRoot(path, root)
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

func setBoolConfigValue(root *yaml.Node, key string, value bool) error {
	mapping := rootMapping(root)
	if mapping == nil {
		return fmt.Errorf("config.yaml 根节点不是 mapping")
	}
	text := "false"
	if value {
		text = "true"
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Value == key {
			v.Kind = yaml.ScalarNode
			v.Tag = "!!bool"
			v.Value = text
			return nil
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text},
	)
	return nil
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
