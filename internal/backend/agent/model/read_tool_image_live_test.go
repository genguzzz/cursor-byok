package modeladapter

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const dsc00868Path = "/Users/gugen/Pictures/DSC00868.png"

func TestCompressRealDSC00868AndNormalizeToImageURL(t *testing.T) {
	if _, err := os.Stat(dsc00868Path); err != nil {
		t.Skipf("real image missing: %v", err)
	}

	raw, err := os.ReadFile(dsc00868Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) < 10*1024*1024 {
		t.Fatalf("expected large source image, got %d bytes", len(raw))
	}

	compressed, err := CompressReadImageForReplay(dsc00868Path, raw, ReadToolImageReplayLimit)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	t.Logf("DSC00868.png %d bytes -> compressed jpeg %d bytes", len(raw), len(compressed))
	if len(compressed) == 0 || len(compressed) > ReadToolImageReplayLimit {
		t.Fatalf("compressed size out of range: %d", len(compressed))
	}
	if compressed[0] != 0xFF || compressed[1] != 0xD8 || compressed[2] != 0xFF {
		t.Fatalf("compressed payload is not jpeg magic")
	}

	imageJSON := readSuccessJSON(dsc00868Path, compressed)
	if !ReadToolResultHasImageData(imageJSON) {
		t.Fatal("compressed Read JSON should be detected as image data")
	}

	messages := []Message{
		{Role: "user", Content: "描述这张图片里有什么，用中文简短回答"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{{
				ID:   "call_read_dsc",
				Type: "function",
				Function: ToolCallFunctionShape{
					Name:      "Read",
					Arguments: `{"path":"/Users/gugen/Pictures/DSC00868.png"}`,
				},
			}},
		},
		{
			Role:       "tool",
			Name:       "Read",
			ToolCallID: "call_read_dsc",
			Content:    imageJSON,
		},
		{Role: "user", Content: "根据刚才 Read 到的图片，用一句话描述画面，必须提到人数和场景氛围"},
	}
	normalized, err := normalizeOpenAIProviderMessages(messages, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	toolContent, ok := normalized[2]["content"].([]map[string]any)
	if !ok || len(toolContent) != 1 || toolContent[0]["type"] != "image_url" {
		t.Fatalf("expected tool image_url parts, got %#v", normalized[2]["content"])
	}
	imageURL, _ := toolContent[0]["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("unexpected data url: %s...", url[:min(80, len(url))])
	}
	b64 := strings.TrimPrefix(url, "data:image/jpeg;base64,")
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode data url: %v", err)
	}
	if len(decoded) != len(compressed) {
		t.Fatalf("data url bytes=%d compressed=%d", len(decoded), len(compressed))
	}
}

func TestLiveGLMVisionRecognizesDSC00868ViaReadToolImageURL(t *testing.T) {
	if os.Getenv("BYOK_LIVE_VISION") != "1" && os.Getenv("CB_LIVE") != "1" {
		t.Skip("set BYOK_LIVE_VISION=1 to run live vision recognition")
	}
	if _, err := os.Stat(dsc00868Path); err != nil {
		t.Fatalf("real image required: %v", err)
	}

	cfg, err := loadLocalAssistantConfigForLiveTest()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	adapterCfg, ok := findModelAdapter(cfg, "glm-5.2-ioa")
	if !ok {
		t.Fatal("glm-5.2-ioa adapter not found in config.yaml")
	}
	if strings.TrimSpace(adapterCfg.APIKey) == "" {
		t.Fatal("glm api key empty")
	}

	compressed, err := LoadAndCompressReadImageFile(dsc00868Path, ReadToolImageReplayLimit)
	if err != nil {
		t.Fatalf("compress real image: %v", err)
	}
	t.Logf("live vision payload jpeg bytes=%d", len(compressed))

	imageJSON := readSuccessJSON(dsc00868Path, compressed)
	messages := []Message{
		{Role: "user", Content: "/Users/gugen/Pictures/DSC00868.png 请理解这张图"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{{
				ID:   "call_read_dsc_live",
				Type: "function",
				Function: ToolCallFunctionShape{
					Name:      "Read",
					Arguments: `{"path":"/Users/gugen/Pictures/DSC00868.png"}`,
				},
			}},
		},
		{
			Role:       "tool",
			Name:       "Read",
			ToolCallID: "call_read_dsc_live",
			Content:    imageJSON,
		},
	}

	// 先确认出站 normalize 真的变成 image_url，再打真实模型。
	normalized, err := normalizeOpenAIProviderMessages(messages, false)
	if err != nil {
		t.Fatalf("normalize before live: %v", err)
	}
	if _, ok := normalized[2]["content"].([]map[string]any); !ok {
		t.Fatalf("live request tool content is not multimodal: %T", normalized[2]["content"])
	}

	adapter := NewCodeBuddyAdapter()
	req := StreamRequest{
		BaseURL:                   adapterCfg.BaseURL,
		APIKey:                    adapterCfg.APIKey,
		Proxy:                     adapterCfg.Proxy,
		ProviderModelID:           adapterCfg.ModelID,
		ModelID:                   adapterCfg.ModelID,
		OpenAIEndpoint:            "/custom",
		ConversationID:            "live-vision-dsc00868",
		RequestID:                 "live-vision-req-001",
		ModelCallID:               "live-vision-call-001",
		Messages:                  messages,
		MaxTokens:                 256,
		ReasoningEffort:           "low",
		ProviderStreamIdleTimeout: 90 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var text strings.Builder
	err = adapter.Stream(ctx, req, func(ev ModelEvent) error {
		if ev.Kind == ModelEventKindTextDelta && ev.Text != "" {
			text.WriteString(ev.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("live vision stream failed: %v", err)
	}
	reply := strings.TrimSpace(text.String())
	t.Logf("live glm reply: %s", reply)
	if reply == "" {
		t.Fatal("empty model reply")
	}
	lower := strings.ToLower(reply)
	visionHints := []string{"人", "两", "夜", "背影", "灯", "坐", "景"}
	hit := 0
	for _, hint := range visionHints {
		if strings.Contains(reply, hint) || strings.Contains(lower, hint) {
			hit++
		}
	}
	if hit < 2 {
		t.Fatalf("reply does not look like real vision recognition (hits=%d): %s", hit, reply)
	}
}

type liveConfigFile struct {
	ModelAdapters []struct {
		DisplayName string `yaml:"displayName"`
		Type        string `yaml:"type"`
		BaseURL     string `yaml:"baseURL"`
		APIKey      string `yaml:"apiKey"`
		Proxy       string `yaml:"proxy"`
		ModelID     string `yaml:"modelID"`
	} `yaml:"modelAdapters"`
}

type liveAdapter struct {
	DisplayName string
	BaseURL     string
	APIKey      string
	Proxy       string
	ModelID     string
}

func loadLocalAssistantConfigForLiveTest() (liveConfigFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return liveConfigFile{}, err
	}
	path := filepath.Join(home, ".cursor-local-assistant-v2", "config.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return liveConfigFile{}, err
	}
	var cfg liveConfigFile
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return liveConfigFile{}, err
	}
	return cfg, nil
}

func findModelAdapter(cfg liveConfigFile, modelID string) (liveAdapter, bool) {
	want := strings.TrimSpace(modelID)
	for _, item := range cfg.ModelAdapters {
		if strings.TrimSpace(item.ModelID) != want {
			continue
		}
		return liveAdapter{
			DisplayName: item.DisplayName,
			BaseURL:     item.BaseURL,
			APIKey:      item.APIKey,
			Proxy:       item.Proxy,
			ModelID:     item.ModelID,
		}, true
	}
	return liveAdapter{}, false
}
