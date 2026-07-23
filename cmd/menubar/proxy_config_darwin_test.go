//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

const testProxyAddr = "http://127.0.0.1:9090"

func testConfigYAML() string {
	return `log: false
providerStreamIdleTimeout: 240
backendListenAddr: 127.0.0.1:18090
proxyListenAddr: 127.0.0.1:18080
modelAdapters:
    - displayName: ModelA
      type: codebuddy
      baseURL: https://example.com
      apiKey: key-a
      tooltipData: Model A
      modelID: model-a
      reasoningEffort: medium
      openAIEndpoint: /custom
      openAIExtraParamsEnabled: false
      openAIExtraParamsJSON: ""
      customHeadersEnabled: false
      customHeadersJSON: '{}'
      anthropicExtraParamsEnabled: false
      anthropicExtraParamsJSON: ""
      contextWindowTokens: 128000
      maxCompletionTokens: 0
      anthropicMaxTokens: 0
      thinkingBudgetTokens: 0
    - displayName: ModelB
      type: openai
      baseURL: https://example2.com
      apiKey: key-b
      tooltipData: Model B
      modelID: model-b
      reasoningEffort: high
      openAIEndpoint: /v1/chat/completions
      openAIExtraParamsEnabled: false
      openAIExtraParamsJSON: ""
      customHeadersEnabled: false
      customHeadersJSON: '{}'
      anthropicExtraParamsEnabled: false
      anthropicExtraParamsJSON: ""
      contextWindowTokens: 64000
      maxCompletionTokens: 0
      anthropicMaxTokens: 0
      thinkingBudgetTokens: 0
routing:
    mode: local
homeMetrics:
    includeCacheWriteInHitRate: false
`
}

func writeTestConfig(t *testing.T, dir string, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

// 基础场景：yaml 中无 proxy 字段 → 初始读不到，开启后写入并读到，关闭后删除并读不到。
func TestWriteProxyConfig_InsertAndRemove(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, testConfigYAML())

	// 1. 开启前：读不到 proxy
	enabled, err := readProxyEnabledFromFile(cfgPath)
	if err != nil {
		t.Fatalf("read before enable: %v", err)
	}
	if enabled {
		t.Fatal("expected proxy not enabled before write")
	}

	// 2. 开启：写入 proxy 到所有 adapter
	if err := writeProxyConfigToFile(cfgPath, true); err != nil {
		t.Fatalf("write enable: %v", err)
	}

	// 3. 开启后：读到 proxy 开启
	enabled, err = readProxyEnabledFromFile(cfgPath)
	if err != nil {
		t.Fatalf("read after enable: %v", err)
	}
	if !enabled {
		t.Fatal("expected proxy enabled after write")
	}

	// 4. 验证所有 adapter 都有 proxy 字段
	root, err := loadConfigRoot(cfgPath)
	if err != nil {
		t.Fatalf("load after enable: %v", err)
	}
	items := findModelAdapterItems(root)
	if len(items) != 2 {
		t.Fatalf("expected 2 adapters, got %d", len(items))
	}
	for i, item := range items {
		if v := proxyValueOf(item); v != testProxyAddr {
			t.Errorf("adapter %d: expected proxy=%q, got %q", i, testProxyAddr, v)
		}
	}

	// 5. 关闭：删除所有 proxy 字段
	if err := writeProxyConfigToFile(cfgPath, false); err != nil {
		t.Fatalf("write disable: %v", err)
	}

	// 6. 关闭后：读不到 proxy
	enabled, err = readProxyEnabledFromFile(cfgPath)
	if err != nil {
		t.Fatalf("read after disable: %v", err)
	}
	if enabled {
		t.Fatal("expected proxy not enabled after disable")
	}

	// 7. 验证所有 adapter 无 proxy 字段
	root, err = loadConfigRoot(cfgPath)
	if err != nil {
		t.Fatalf("load after disable: %v", err)
	}
	items = findModelAdapterItems(root)
	if len(items) != 2 {
		t.Fatalf("expected 2 adapters after disable, got %d", len(items))
	}
	for i, item := range items {
		if v := proxyValueOf(item); v != "" {
			t.Errorf("adapter %d: expected proxy empty after disable, got %q", i, v)
		}
	}
}

// 场景：yaml 中已有 proxy 字段（值为空），开启时更新，关闭时清空。
func TestWriteProxyConfig_UpdateExisting(t *testing.T) {
	dir := t.TempDir()
	baseYAML := testConfigYAML()

	// 先插入 proxy: ""（模拟已有空值）
	root, err := loadConfigRoot(writeTestConfig(t, dir, baseYAML))
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	items := findModelAdapterItems(root)
	for _, item := range items {
		setProxyOnAdapterNode(item, "")
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := writeConfigRoot(cfgPath, root); err != nil {
		t.Fatalf("write base with empty proxy: %v", err)
	}

	// 1. 空 proxy 不应被识别为开启
	enabled, err := readProxyEnabledFromFile(cfgPath)
	if err != nil {
		t.Fatalf("read with empty proxy: %v", err)
	}
	if enabled {
		t.Fatal("expected proxy not enabled with empty value")
	}

	// 2. 开启：更新为实际代理地址
	if err := writeProxyConfigToFile(cfgPath, true); err != nil {
		t.Fatalf("write enable (update): %v", err)
	}
	enabled, err = readProxyEnabledFromFile(cfgPath)
	if err != nil {
		t.Fatalf("read after update enable: %v", err)
	}
	if !enabled {
		t.Fatal("expected proxy enabled after update")
	}

	// 3. 关闭：删除字段
	if err := writeProxyConfigToFile(cfgPath, false); err != nil {
		t.Fatalf("write disable: %v", err)
	}
	enabled, err = readProxyEnabledFromFile(cfgPath)
	if err != nil {
		t.Fatalf("read after disable: %v", err)
	}
	if enabled {
		t.Fatal("expected proxy not enabled after disable")
	}
}

// 场景：文件不存在时 readProxyEnabledFromFile 返回 false 不报错。
func TestReadProxyEnabled_FileNotExist(t *testing.T) {
	enabled, err := readProxyEnabledFromFile("/tmp/nonexistent-config-test.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enabled {
		t.Fatal("expected false for non-existent file")
	}
}

// 场景：只写 proxy 不改其他字段，yaml 序列化/反序列化后其他字段不变。
func TestWriteProxyConfig_PreserveOtherFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, testConfigYAML())

	if err := writeProxyConfigToFile(cfgPath, true); err != nil {
		t.Fatalf("write enable: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse after write: %v", err)
	}

	// 验证顶层字段不变
	if v, ok := parsed["backendListenAddr"]; !ok || v != "127.0.0.1:18090" {
		t.Errorf("backendListenAddr changed: %v", v)
	}
	if v, ok := parsed["routing"]; !ok {
		t.Errorf("routing section missing: %v", v)
	}

	// 验证 adapter 字段
	adapters, ok := parsed["modelAdapters"].([]any)
	if !ok {
		t.Fatal("modelAdapters missing or not a list")
	}
	if len(adapters) != 2 {
		t.Fatalf("expected 2 adapters, got %d", len(adapters))
	}
	for i, raw := range adapters {
		adapter, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("adapter %d not a map", i)
		}
		if v, ok := adapter["displayName"]; !ok {
			t.Errorf("adapter %d: displayName missing", i)
		} else if i == 0 && v != "ModelA" {
			t.Errorf("adapter %d: displayName=%v", i, v)
		}
		if v, ok := adapter["proxy"]; !ok {
			t.Errorf("adapter %d: proxy missing after enable", i)
		} else if v != testProxyAddr {
			t.Errorf("adapter %d: proxy=%v", i, v)
		}
	}
}

// 场景：idempotent — 连续两次 enable 不报错，proxy 值不变。
func TestWriteProxyConfig_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, testConfigYAML())

	for i := 0; i < 2; i++ {
		if err := writeProxyConfigToFile(cfgPath, true); err != nil {
			t.Fatalf("write enable iteration %d: %v", i, err)
		}
	}

	root, err := loadConfigRoot(cfgPath)
	if err != nil {
		t.Fatalf("load after idempotent writes: %v", err)
	}
	items := findModelAdapterItems(root)
	for i, item := range items {
		if v := proxyValueOf(item); v != testProxyAddr {
			t.Errorf("adapter %d: proxy=%q after idempotent writes", i, v)
		}
	}
}