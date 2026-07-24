package forwarder

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func TestPromoteReadToolImagesBeforePromptGuard(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0xFF, 0xD9}
	// 构造超过 promptGuardCompiledMessageChars 的合法图片 JSON。
	big := make([]byte, 100*1024)
	copy(big, jpeg)
	for i := len(jpeg); i < len(big); i++ {
		big[i] = byte(i % 251)
	}
	big[0], big[1], big[2] = 0xFF, 0xD8, 0xFF
	payload, err := json.Marshal(map[string]any{
		"success": map[string]any{
			"path": "/tmp/yt_thumbs/thumb_1.jpg",
			"data": base64.StdEncoding.EncodeToString(big),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(payload) <= promptGuardCompiledMessageChars {
		t.Fatalf("fixture too small for guard: %d", len(payload))
	}

	compiled := CompiledConversation{
		Messages: []modeladapter.Message{{
			Role:       "tool",
			Name:       "Read",
			ToolCallID: "call_1",
			Content:    string(payload),
		}},
	}
	got := guardCompiledConversationForProvider(compiled)
	msg := got.Messages[0]
	if strings.Contains(msg.Content, "[truncated:") {
		t.Fatalf("image tool content should not be text-truncated, got %q", msg.Content[:min(120, len(msg.Content))])
	}
	if len(msg.ContentParts) != 1 || msg.ContentParts[0].Image == nil {
		t.Fatalf("expected promoted image content part, content=%q parts=%d", msg.Content, len(msg.ContentParts))
	}
	if len(msg.ContentParts[0].Image.Data) != len(big) {
		t.Fatalf("image bytes=%d want=%d", len(msg.ContentParts[0].Image.Data), len(big))
	}
	if strings.TrimSpace(msg.Content) != "" {
		t.Fatalf("promoted image should clear text content, got %q", msg.Content)
	}
}

func TestPromptGuardReplacesCorruptedReadImageJSON(t *testing.T) {
	corrupted := `{"success":{"data":"/9j/abc` + "\n\n[truncated: compiled.tool exceeded 120000 chars; kept head and tail from 200000 chars]\n\n" + `xyz","path":"a.jpg"}}`
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{{
			Role:    "tool",
			Name:    "Read",
			Content: corrupted,
		}},
	}
	got := guardCompiledConversationForProvider(compiled)
	if !strings.Contains(got.Messages[0].Content, "corrupted by text truncation") {
		t.Fatalf("want corruption error replacement, got %q", got.Messages[0].Content)
	}
}
