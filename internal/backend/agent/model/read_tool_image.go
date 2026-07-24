package modeladapter

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// ReadToolResultHasImageData 判断 Read 工具 replay JSON 是否携带可转多模态的图片数据。
func ReadToolResultHasImageData(content string) bool {
	_, ok := readToolResultImageDataURL(content)
	return ok
}

// ReadToolResultImageContentPart 把 Read 工具图片 JSON 提升为 ContentPart，供 compile/estimate 使用。
func ReadToolResultImageContentPart(content string) (ContentPart, bool) {
	dataURL, ok := readToolResultImageDataURL(content)
	if !ok {
		return ContentPart{}, false
	}
	mime, payload, ok := splitImageDataURL(dataURL)
	if !ok {
		return ContentPart{}, false
	}
	path := ""
	var wrapper map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &wrapper); err == nil {
		if success, _ := wrapper["success"].(map[string]any); success != nil {
			path, _ = success["path"].(string)
		}
	}
	return ContentPart{
		Type: contentPartTypeImage,
		Image: &ImageContent{
			MIMEType: mime,
			Path:     path,
			Data:     payload,
		},
	}, true
}

func openAIReadToolImageContent(toolName string, content string) (any, bool) {
	if strings.TrimSpace(toolName) != "Read" {
		return nil, false
	}
	dataURL, ok := readToolResultImageDataURL(content)
	if !ok {
		return nil, false
	}
	return []map[string]any{{
		"type": "image_url",
		"image_url": map[string]any{
			"url": dataURL,
		},
	}}, true
}

func readToolResultImageDataURL(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	if strings.Contains(trimmed, "[truncated:") {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	success, _ := payload["success"].(map[string]any)
	if success == nil {
		return "", false
	}
	if text, _ := success["content"].(string); strings.Contains(text, "[truncated:") {
		return "", false
	}
	dataText, _ := success["data"].(string)
	if strings.TrimSpace(dataText) == "" {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(dataText)), "data:image/") {
		return strings.TrimSpace(dataText), true
	}
	imageBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataText))
	if err != nil {
		return "", false
	}
	if len(imageBytes) == 0 || !isImagePayload(imageBytes) {
		return "", false
	}
	path, _ := success["path"].(string)
	mime := normalizeImageMIMEType("", path, imageBytes)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(imageBytes), true
}

func splitImageDataURL(dataURL string) (mime string, payload []byte, ok bool) {
	trimmed := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return "", nil, false
	}
	comma := strings.Index(trimmed, ",")
	if comma <= 5 {
		return "", nil, false
	}
	meta := trimmed[len("data:"):comma]
	raw := trimmed[comma+1:]
	mime = strings.TrimSpace(strings.Split(meta, ";")[0])
	if mime == "" {
		mime = "image/jpeg"
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) == 0 {
		return "", nil, false
	}
	return mime, decoded, true
}

func isImagePayload(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	mime := normalizeImageMIMEType("", "", payload)
	return strings.HasPrefix(mime, "image/")
}
