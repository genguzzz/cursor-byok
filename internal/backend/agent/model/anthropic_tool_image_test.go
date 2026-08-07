package modeladapter

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestAnthropicToolResultContentMatchesTclaudeCLINestedImage(t *testing.T) {
	jpeg := jpegFixture()
	msg := Message{
		Role:       "tool",
		Name:       "Read",
		ToolCallID: "call_read_image",
		Content:    "",
		ContentParts: []ContentPart{{
			Type: "image",
			Image: &ImageContent{
				MIMEType: "image/jpeg",
				Path:     "/tmp/photo.jpg",
				Data:     jpeg,
			},
		}},
	}

	content, trailing, err := anthropicToolResultContent(msg)
	if err != nil {
		t.Fatalf("anthropicToolResultContent: %v", err)
	}
	blocks, ok := content.([]map[string]any)
	if !ok || len(blocks) != 1 || blocks[0]["type"] != "image" {
		t.Fatalf("tool_result.content should be nested image blocks, got %#v", content)
	}
	source, _ := blocks[0]["source"].(anthropicBase64ImageSource)
	if source.Type != "base64" || source.MediaType != "image/jpeg" {
		t.Fatalf("source = %#v", source)
	}
	data := source.Data
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 {
		t.Fatalf("bad image payload: err=%v len=%d", err, len(decoded))
	}
	if !isJFIFJPEGPayload(decoded) {
		t.Fatalf("expected JFIF JPEG like CLI, magic=%x", decoded[:min(20, len(decoded))])
	}
	if len(trailing) != 1 || trailing[0]["type"] != "text" {
		t.Fatalf("want trailing [Image: ...] text, got %#v", trailing)
	}
	text, _ := trailing[0]["text"].(string)
	if !strings.Contains(text, "[Image:") && !strings.Contains(text, "Image attached") {
		t.Fatalf("trailing text = %q", text)
	}
}

func TestAnthropicToolResultContentFromReadJSONFallback(t *testing.T) {
	jpeg := jpegFixture()
	msg := Message{
		Role:       "tool",
		Name:       "Read",
		ToolCallID: "call_read_image",
		Content:    readSuccessJSON("/tmp/photo.jpg", jpeg),
	}

	content, trailing, err := anthropicToolResultContent(msg)
	if err != nil {
		t.Fatalf("anthropicToolResultContent: %v", err)
	}
	blocks, ok := content.([]map[string]any)
	if !ok || len(blocks) != 1 || blocks[0]["type"] != "image" {
		t.Fatalf("want nested image from Read JSON, got %#v", content)
	}
	if len(trailing) == 0 {
		t.Fatalf("expected trailing image note")
	}
}

func TestNormalizeAnthropicProviderMessagesNestsToolImageLikeCLI(t *testing.T) {
	jpeg := jpegFixture()
	input := []Message{
		{Role: "user", Content: "describe the photo"},
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{{
				ID:   "call_1",
				Type: "function",
				Function: ToolCallFunctionShape{
					Name:      "Read",
					Arguments: `{"path":"/tmp/photo.jpg"}`,
				},
			}},
		},
		{
			Role:       "tool",
			Name:       "Read",
			ToolCallID: "call_1",
			ContentParts: []ContentPart{{
				Type: "image",
				Image: &ImageContent{
					MIMEType: "image/jpeg",
					Path:     "/tmp/photo.jpg",
					Data:     jpeg,
				},
			}},
		},
	}

	_, messages, err := normalizeAnthropicProviderMessages(input, false, true)
	if err != nil {
		t.Fatalf("normalizeAnthropicProviderMessages: %v", err)
	}
	foundNested := false
	foundTopLevelImage := false
	foundTrailing := false
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "user" {
			continue
		}
		for _, block := range message.Content {
			switch strings.TrimSpace(anthropicStringField(block, "type")) {
			case "tool_result":
				inner, ok := block["content"].([]map[string]any)
				if !ok {
					t.Fatalf("tool_result content should be blocks, got %#v", block["content"])
				}
				for _, part := range inner {
					if strings.TrimSpace(anthropicStringField(part, "type")) == "image" {
						foundNested = true
					}
				}
			case "image":
				foundTopLevelImage = true
			case "text":
				text := anthropicStringField(block, "text")
				if strings.Contains(text, "[Image:") || strings.Contains(text, "Image attached") {
					foundTrailing = true
				}
			}
		}
	}
	if !foundNested {
		t.Fatalf("expected nested tool_result image like tclaude CLI: %#v", messages)
	}
	if foundTopLevelImage {
		t.Fatalf("image must stay nested inside tool_result, not top-level sibling: %#v", messages)
	}
	if !foundTrailing {
		t.Fatalf("expected trailing [Image: ...] text after tool_result: %#v", messages)
	}
}

func TestAnthropicToolResultContentPlainTextUnchanged(t *testing.T) {
	msg := Message{
		Role:       "tool",
		Name:       "Read",
		ToolCallID: "call_1",
		Content:    `{"success":{"path":"a.go","content":"package main\n"}}`,
	}
	content, trailing, err := anthropicToolResultContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := content.(string)
	if !ok || text != msg.Content {
		t.Fatalf("plain tool content should stay string, got %#v", content)
	}
	if len(trailing) != 0 {
		t.Fatalf("plain tool should not emit trailing blocks, got %#v", trailing)
	}
}

func TestRelocateAnthropicImagesDoesNotHoistNestedToolResultImages(t *testing.T) {
	jpeg := jpegFixture()
	encoded := base64.StdEncoding.EncodeToString(jpeg)
	messages := []anthropicMessage{
		{
			Role: "user",
			Content: []map[string]any{{
				"type": "text",
				"text": "earlier",
			}, {
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": "image/jpeg",
					"data":       encoded,
				},
			}},
		},
		{
			Role: "user",
			Content: []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": "call_1",
				"content": []map[string]any{{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/jpeg",
						"data":       encoded,
					},
				}},
			}},
		},
	}
	got := relocateAnthropicImagesToLastUserMessage(messages)
	if len(got) != 2 {
		t.Fatalf("want 2 messages, got %#v", got)
	}
	// first user paste image moved to last
	firstHasTopImage := false
	for _, b := range got[0].Content {
		if isAnthropicImageBlock(b) {
			firstHasTopImage = true
		}
	}
	if firstHasTopImage {
		t.Fatalf("top-level paste image should relocate off first message: %#v", got[0])
	}
	last := got[1].Content
	nestedOK := false
	topOK := false
	for _, b := range last {
		switch anthropicStringField(b, "type") {
		case "tool_result":
			inner, _ := b["content"].([]map[string]any)
			for _, p := range inner {
				if isAnthropicImageBlock(p) {
					nestedOK = true
				}
			}
		case "image":
			topOK = true
		}
	}
	if !nestedOK || !topOK {
		t.Fatalf("last user should keep nested tool image + relocated top image: %#v", last)
	}
}

func TestEnsureAnthropicMessageImagesJPEGRewritesPNGBytesAndMIME(t *testing.T) {
	png := mustPNGFixture(t)
	msg := Message{
		Role: "tool",
		Name: "Read",
		ContentParts: []ContentPart{{
			Type: "image",
			Image: &ImageContent{
				MIMEType: "image/png",
				Path:     "/tmp/banner.png",
				Data:     png,
			},
		}},
	}
	got := ensureAnthropicMessageImagesJPEG(msg)
	img := got.ContentParts[0].Image
	if img == nil || img.MIMEType != "image/jpeg" {
		t.Fatalf("MIMEType = %#v", img)
	}
	if !isJPEGPayload(img.Data) {
		t.Fatalf("payload still not JPEG, magic=%v", img.Data[:min(4, len(img.Data))])
	}
	if !isJFIFJPEGPayload(img.Data) {
		t.Fatalf("Anthropic JPEG must include JFIF APP0 like tclaude CLI, magic=%x", img.Data[:min(20, len(img.Data))])
	}

	content, _, err := anthropicToolResultContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	blocks := content.([]map[string]any)
	source := blocks[0]["source"].(anthropicBase64ImageSource)
	raw, err := base64.StdEncoding.DecodeString(source.Data)
	if err != nil {
		t.Fatal(err)
	}
	if source.MediaType != "image/jpeg" || !isJFIFJPEGPayload(raw) {
		t.Fatalf("tool image still mismatched: media=%v magic=%x", source.MediaType, raw[:min(20, len(raw))])
	}
}

func TestEnsureAnthropicUpscalesTinyBannerLikeCLIReadable(t *testing.T) {
	png := mustNarrowPNGFixture(t, 714, 82)
	msg := Message{
		Role: "tool",
		Name: "Read",
		ContentParts: []ContentPart{{
			Type: "image",
			Image: &ImageContent{
				MIMEType: "image/png",
				Path:     "/tmp/banner.png",
				Data:     png,
			},
		}},
	}
	content, trailing, err := anthropicToolResultContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	blocks := content.([]map[string]any)
	raw, err := base64.StdEncoding.DecodeString(blocks[0]["source"].(anthropicBase64ImageSource).Data)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Height < anthropicVisionMinSide || cfg.Width < anthropicVisionMinSide {
		t.Fatalf("expected upscaled min side >= %d, got %dx%d", anthropicVisionMinSide, cfg.Width, cfg.Height)
	}
	if !isJFIFJPEGPayload(raw) {
		t.Fatalf("upscaled JPEG missing JFIF header: %x", raw[:min(20, len(raw))])
	}
	if len(trailing) != 1 {
		t.Fatalf("trailing = %#v", trailing)
	}
	text, _ := trailing[0]["text"].(string)
	if !strings.Contains(text, "original 714x82") || !strings.Contains(text, fmt.Sprintf("displayed at %dx%d", cfg.Width, cfg.Height)) {
		t.Fatalf("trailing should report original vs displayed, got %q (disp=%dx%d)", text, cfg.Width, cfg.Height)
	}
}

func TestNormalizeAnthropicKeepsUserPasteTopLevelImagesLikeCLI(t *testing.T) {
	jpeg := jpegFixture()
	input := []Message{
		{
			Role:    "user",
			Content: "what is this",
			ContentParts: []ContentPart{{
				Type: "image",
				Image: &ImageContent{
					MIMEType: "image/jpeg",
					Path:     "/tmp/banner.jpg",
					Data:     jpeg,
				},
			}},
		},
	}
	_, messages, err := normalizeAnthropicProviderMessages(input, false, true)
	if err != nil {
		t.Fatal(err)
	}
	foundTop := false
	foundSourceNote := false
	for _, message := range messages {
		for _, block := range message.Content {
			if isAnthropicImageBlock(block) {
				foundTop = true
			}
			if anthropicStringField(block, "type") == "text" && strings.Contains(anthropicStringField(block, "text"), "[Image: source:") {
				foundSourceNote = true
			}
		}
	}
	if !foundTop || !foundSourceNote {
		t.Fatalf("CLI paste keeps top-level image + source note, got %#v", messages)
	}
}

func mustPNGFixture(t *testing.T) []byte {
	t.Helper()
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustNarrowPNGFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, image.Black)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
