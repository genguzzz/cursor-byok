//go:build darwin

package main

import (
	"fmt"
	"os"
	"strings"

	"cursor/internal/appdata"

	"gopkg.in/yaml.v3"
)

const proxyConfigField = "proxy"

// readProxyEnabledFromFile 从 config.yaml 读取 proxy 是否启用：只要任意 model
// adapter 项的 proxy 字段匹配本地代理地址即认为开启。文件不存在时返回 false。
func readProxyEnabledFromFile(path string) (bool, error) {
	root, err := loadConfigRoot(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	items := findModelAdapterItems(root)
	for _, item := range items {
		if proxyValueOf(item) == proxyAddr {
			return true, nil
		}
	}
	return false, nil
}

// writeProxyConfigToFile 为 config.yaml 中每个 model adapter 项插入/更新/删除
// proxy 字段。使用 yaml.Node 树形 API 以保留所有键的顺序、注释与其他字段。
func writeProxyConfigToFile(path string, enable bool) error {
	root, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	items := findModelAdapterItems(root)
	if len(items) == 0 {
		return fmt.Errorf("config.yaml 中找不到 modelAdapters 列表")
	}
	for _, item := range items {
		if enable {
			setProxyOnAdapterNode(item, proxyAddr)
		} else {
			removeProxyFromAdapterNode(item)
		}
	}
	return writeConfigRoot(path, root)
}

// loadConfigRoot 读取并解析 yaml 文件为根节点。
func loadConfigRoot(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("解析 config.yaml 失败: %w", err)
	}
	if root.Kind == 0 {
		root.Kind = yaml.DocumentNode
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config.yaml 根节点不是 mapping")
	}
	return &root, nil
}

// writeConfigRoot 将根节点序列化为 yaml 并原子写回磁盘。
func writeConfigRoot(path string, root *yaml.Node) error {
	data, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("序列化 config.yaml 失败: %w", err)
	}
	if err := appdata.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("保存 config.yaml 失败: %w", err)
	}
	return nil
}

// findModelAdapterItems 在根节点中定位 modelAdapters 序列，并返回每个条目的 mapping 节点。
func findModelAdapterItems(root *yaml.Node) []*yaml.Node {
	if root == nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	list := findChildSequence(doc, "modelAdapters")
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}
	items := make([]*yaml.Node, 0, len(list.Content))
	for _, item := range list.Content {
		if item.Kind == yaml.MappingNode {
			items = append(items, item)
		}
	}
	return items
}

// findChildSequence 在 mapping 节点中按 key 查找子 sequence 节点。
func findChildSequence(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Value == key {
			return v
		}
	}
	return nil
}

// proxyValueOf 读取 mapping 节点中 proxy 字段的字符串值；缺失或非字符串返回空。
func proxyValueOf(item *yaml.Node) string {
	if item == nil || item.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		k := item.Content[i]
		if k.Value != proxyConfigField {
			continue
		}
		v := item.Content[i+1]
		return strings.TrimSpace(v.Value)
	}
	return ""
}

// setProxyOnAdapterNode 在 mapping 节点中插入或更新 proxy 字段为给定值，保持原有顺序。
func setProxyOnAdapterNode(item *yaml.Node, value string) {
	if item == nil || item.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == proxyConfigField {
			v := item.Content[i+1]
			v.Value = value
			v.Tag = "!!str"
			v.Kind = yaml.ScalarNode
			return
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: proxyConfigField}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	item.Content = append(item.Content, keyNode, valNode)
}

// removeProxyFromAdapterNode 从 mapping 节点中删除 proxy 键值对。
func removeProxyFromAdapterNode(item *yaml.Node) {
	if item == nil || item.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == proxyConfigField {
			item.Content = append(item.Content[:i], item.Content[i+2:]...)
			return
		}
	}
}