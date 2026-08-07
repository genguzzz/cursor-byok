package modeladapter

import (
	"strings"
	"testing"
)

func TestRewriteCodeBuddyUserImagesToPathFallback(t *testing.T) {
	path := "/tmp/paste.png"
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	messages := []Message{{
		Role: "user",
		Content: `<user_query>
图片里有什么
</user_query>`,
		ContentParts: []ContentPart{
			{Type: "text", Text: `<user_query>
图片里有什么
</user_query>`},
			{Type: "image", Image: &ImageContent{MIMEType: "image/png", Path: path, Data: png}},
		},
	}, {
		Role:       "tool",
		Name:       "Read",
		ToolCallID: "c1",
		ContentParts: []ContentPart{{
			Type:  "image",
			Image: &ImageContent{MIMEType: "image/png", Path: path, Data: png},
		}},
	}}

	got := rewriteUserInlineImagesToPathFallback(messages)
	user := got[0]
	for _, part := range user.ContentParts {
		if normalizeContentPartType(part.Type) == contentPartTypeImage {
			t.Fatalf("user image ContentPart should be stripped, got %#v", user.ContentParts)
		}
	}
	if !strings.Contains(user.Content, path) {
		t.Fatalf("user content should keep path, got %q", user.Content)
	}
	if !strings.Contains(user.Content, "call the Read tool on each path") {
		t.Fatalf("user content missing Read hint: %q", user.Content)
	}
	// tool message images must remain (Read→image_url path)
	if len(got[1].ContentParts) != 1 || got[1].ContentParts[0].Image == nil {
		t.Fatalf("tool image ContentParts must stay intact: %#v", got[1].ContentParts)
	}
}

func TestRewriteCodeBuddyUserImagesToPathFallbackNoopWithoutImages(t *testing.T) {
	messages := []Message{{Role: "user", Content: "hello"}}
	got := rewriteUserInlineImagesToPathFallback(messages)
	if got[0].Content != "hello" || len(got[0].ContentParts) != 0 {
		t.Fatalf("unexpected rewrite: %#v", got[0])
	}
}
