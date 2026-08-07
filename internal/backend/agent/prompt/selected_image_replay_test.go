package promptengine

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestBuildUserMessageReplayMessageIncludesSelectedImagePathFallback(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	path := "/Users/gengu/Library/Application Support/Cursor/User/workspaceStorage/empty-window/images/image-demo.png"
	userMessage := &agentv1.UserMessage{
		Text: "这个图里有什么",
		SelectedContext: &agentv1.SelectedContext{
			SelectedImages: []*agentv1.SelectedImage{{
				Path:     path,
				MimeType: "image/png",
				DataOrBlobId: &agentv1.SelectedImage_BlobIdWithData_{
					BlobIdWithData: &agentv1.SelectedImage_BlobIdWithData{
						BlobId: []byte("blob-demo"),
						Data:   png,
					},
				},
			}},
		},
	}

	msg, ok := BuildUserMessageReplayMessage(userMessage)
	if !ok {
		t.Fatal("expected replay message")
	}
	if !strings.Contains(msg.Content, "<selected_images>") {
		t.Fatalf("content missing selected_images path fallback: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, `path="`+path+`"`) {
		t.Fatalf("content missing image path: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, `mime_type="image/png"`) {
		t.Fatalf("content missing mime type: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "call the Read tool on each path") {
		t.Fatalf("content missing Read fallback hint: %q", msg.Content)
	}
	if len(msg.ContentParts) < 2 {
		t.Fatalf("want text+image content parts, got %#v", msg.ContentParts)
	}
	hasImage := false
	for _, part := range msg.ContentParts {
		if part.Type == "image" && part.Image != nil && len(part.Image.Data) == len(png) {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("multimodal image ContentPart should remain intact: %#v", msg.ContentParts)
	}
}

func TestBuildUserMessageReplayMessageDedupesSelectedImagePaths(t *testing.T) {
	path := "/tmp/same.png"
	userMessage := &agentv1.UserMessage{
		Text: "see",
		SelectedContext: &agentv1.SelectedContext{
			SelectedImages: []*agentv1.SelectedImage{
				{Path: path, MimeType: "image/png", DataOrBlobId: &agentv1.SelectedImage_Data{Data: []byte{0x89, 0x50}}},
				{Path: path, MimeType: "image/png", DataOrBlobId: &agentv1.SelectedImage_Data{Data: []byte{0x89, 0x50}}},
			},
		},
	}
	msg, ok := BuildUserMessageReplayMessage(userMessage)
	if !ok {
		t.Fatal("expected replay message")
	}
	if strings.Count(msg.Content, `path="`+path+`"`) != 1 {
		t.Fatalf("expected one path entry, got %q", msg.Content)
	}
}
