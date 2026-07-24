package modeladapter

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func jpegFixture() []byte {
	return []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9,
	}
}

func readSuccessJSON(path string, data []byte) string {
	payload, err := json.Marshal(map[string]any{
		"success": map[string]any{
			"path": path,
			"data": base64.StdEncoding.EncodeToString(data),
		},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func TestReadToolResultHasImageData(t *testing.T) {
	imageJSON := readSuccessJSON("/tmp/photo.jpg", jpegFixture())
	if !ReadToolResultHasImageData(imageJSON) {
		t.Fatal("jpeg Read success JSON should be detected as image data")
	}

	textJSON := `{"success":{"path":"a.go","content":"package main\n"}}`
	if ReadToolResultHasImageData(textJSON) {
		t.Fatal("text Read success JSON must not be treated as image data")
	}

	truncatedJSON := `{"success":{"content":"[truncated: Read binary data result exceeded 32768 bytes; showing 0 of 99999 bytes]"}}`
	if ReadToolResultHasImageData(truncatedJSON) {
		t.Fatal("truncated notice must not be treated as image data")
	}
}

func TestOpenAIReadToolImageContent(t *testing.T) {
	imageJSON := readSuccessJSON("/Users/gugen/Pictures/DSC00868.jpg", jpegFixture())
	content, ok := openAIReadToolImageContent("Read", imageJSON)
	if !ok {
		t.Fatal("expected Read image JSON to convert")
	}
	parts, ok := content.([]map[string]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("want one image_url part, got %#v", content)
	}
	if parts[0]["type"] != "image_url" {
		t.Fatalf("type=%v", parts[0]["type"])
	}
	imageURL, _ := parts[0]["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("unexpected data url prefix: %q", url)
	}
	encoded := strings.TrimPrefix(url, "data:image/jpeg;base64,")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode data url: %v", err)
	}
	if string(decoded) != string(jpegFixture()) {
		t.Fatal("decoded image bytes mismatch")
	}

	if _, ok := openAIReadToolImageContent("Shell", imageJSON); ok {
		t.Fatal("non-Read tool must not convert")
	}
	if _, ok := openAIReadToolImageContent("Read", `{"success":{"content":"hello"}}`); ok {
		t.Fatal("text Read must not convert")
	}
}

func TestNormalizeOpenAIProviderMessagesReadImage(t *testing.T) {
	imageJSON := readSuccessJSON("DSC00868.png", jpegFixture())
	messages := []Message{
		{Role: "user", Content: "看看这张图"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{{
				ID:   "call_read_1",
				Type: "function",
				Function: ToolCallFunctionShape{
					Name:      "Read",
					Arguments: `{"path":"DSC00868.png"}`,
				},
			}},
		},
		{
			Role:       "tool",
			Name:       "Read",
			ToolCallID: "call_read_1",
			Content:    imageJSON,
		},
	}

	normalized, err := normalizeOpenAIProviderMessages(messages, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(normalized) != 3 {
		t.Fatalf("len=%d", len(normalized))
	}
	toolMsg := normalized[2]
	if toolMsg["role"] != "tool" {
		t.Fatalf("role=%v", toolMsg["role"])
	}
	content, ok := toolMsg["content"].([]map[string]any)
	if !ok {
		t.Fatalf("tool content should be multimodal parts, got %T %#v", toolMsg["content"], toolMsg["content"])
	}
	if len(content) != 1 || content[0]["type"] != "image_url" {
		t.Fatalf("unexpected multimodal content: %#v", content)
	}
	imageURL, _ := content[0]["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if !strings.Contains(url, ";base64,") {
		t.Fatalf("missing base64 data url: %q", url)
	}
}

func TestNormalizeOpenAIProviderMessagesReadTextUnchanged(t *testing.T) {
	textJSON := `{"success":{"path":"main.go","content":"package main\n"}}`
	messages := []Message{{
		Role:       "tool",
		Name:       "Read",
		ToolCallID: "call_1",
		Content:    textJSON,
	}}
	normalized, err := normalizeOpenAIProviderMessages(messages, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	content, ok := normalized[0]["content"].(string)
	if !ok || content != textJSON {
		t.Fatalf("text Read should stay JSON string, got %#v", normalized[0]["content"])
	}
}

func TestNormalizeOpenAIProviderMessagesUserImagePartsUnchanged(t *testing.T) {
	messages := []Message{{
		Role: "user",
		ContentParts: []ContentPart{{
			Type: contentPartTypeImage,
			Image: &ImageContent{
				MIMEType: "image/jpeg",
				Data:     jpegFixture(),
			},
		}},
	}}
	normalized, err := normalizeOpenAIProviderMessages(messages, false)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	content, ok := normalized[0]["content"].([]map[string]any)
	if !ok || len(content) == 0 || content[0]["type"] != "image_url" {
		t.Fatalf("user ContentParts path should still emit image_url, got %#v", normalized[0]["content"])
	}
}
