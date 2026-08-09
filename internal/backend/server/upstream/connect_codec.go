package upstream

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxConnectFrameBytes = 64 << 20

func decodeProtoPayload(contentType string, body []byte, message proto.Message) error {
	body = maybeHTTPDecompress(nil, body)
	candidates := make([][]byte, 0, 2)
	if payload, err := unwrapRequestPayload(contentType, body); err == nil && len(payload) > 0 {
		candidates = append(candidates, payload)
	}
	if len(body) > 0 {
		candidates = append(candidates, body)
	}
	var lastErr error
	for _, candidate := range candidates {
		proto.Reset(message)
		if err := proto.Unmarshal(candidate, message); err == nil {
			return nil
		} else {
			lastErr = err
		}
		proto.Reset(message)
		if err := protojson.Unmarshal(candidate, message); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		return fmt.Errorf("empty body")
	}
	return fmt.Errorf("protobuf unmarshal ct=%s size=%d: %w", strings.TrimSpace(contentType), len(body), lastErr)
}

func unwrapRequestPayload(contentType string, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	normalized := strings.ToLower(contentType)
	if strings.Contains(normalized, "connect") || looksLikeConnectFrame(body) {
		return unwrapConnectUnary(body)
	}
	return body, nil
}

func looksLikeConnectFrame(body []byte) bool {
	if len(body) < 5 {
		return false
	}
	flags := body[0]
	if flags&^0x03 != 0 {
		return false
	}
	length := binary.BigEndian.Uint32(body[1:5])
	return length > 0 && int(length)+5 <= len(body)
}

func maybeHTTPDecompress(headers http.Header, body []byte) []byte {
	if len(body) < 2 {
		return body
	}
	encoding := ""
	if headers != nil {
		encoding = strings.ToLower(strings.TrimSpace(headers.Get("Content-Encoding")))
	}
	// 始终识别 gzip magic；部分客户端会带非标准 Content-Encoding，或省略该头。
	if !strings.Contains(encoding, "gzip") && !looksLikeGzip(body) {
		return body
	}
	return gunzipBytes(body)
}

func looksLikeGzip(body []byte) bool {
	return len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
}

func gunzipBytes(body []byte) []byte {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxConnectFrameBytes+1))
	if err != nil || len(decoded) == 0 || len(decoded) > maxConnectFrameBytes {
		return body
	}
	return decoded
}

func bodyHead(body []byte, n int) []byte {
	if len(body) <= n {
		return body
	}
	return body[:n]
}

func trimBodyForLog(body []byte) string {
	const limit = 240
	text := strings.ToValidUTF8(string(bodyHead(body, limit)), "")
	text = strings.ReplaceAll(text, "\n", " ")
	if len(body) > limit {
		return text + "..."
	}
	return text
}

func unwrapConnectUnary(body []byte) ([]byte, error) {
	if len(body) < 5 {
		return nil, fmt.Errorf("connect frame too short")
	}
	flags := body[0]
	length := int(binary.BigEndian.Uint32(body[1:5]))
	if length < 0 || length > maxConnectFrameBytes {
		return nil, fmt.Errorf("connect frame length invalid")
	}
	if 5+length > len(body) {
		return nil, fmt.Errorf("connect frame truncated")
	}
	payload := body[5 : 5+length]
	// Connect 规范用 flags&0x01 表示 gzip；同时嗅探 magic，兼容漏标压缩位的客户端。
	if flags&0x01 == 0 && !looksLikeGzip(payload) {
		return payload, nil
	}
	decoded := gunzipBytes(payload)
	if looksLikeGzip(payload) && looksLikeGzip(decoded) {
		return nil, fmt.Errorf("gzip: decompress failed")
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("gzip: empty payload")
	}
	return decoded, nil
}

// encodeRequestProtoPayload 把 protobuf 编成与原请求形态匹配的 unary body（不压缩）。
func encodeRequestProtoPayload(contentType string, originalBody []byte, message proto.Message) ([]byte, string, error) {
	if message == nil {
		return nil, "", fmt.Errorf("nil proto message")
	}
	raw, err := proto.Marshal(message)
	if err != nil {
		return nil, "", err
	}
	normalized := strings.ToLower(strings.TrimSpace(contentType))
	useConnect := strings.Contains(normalized, "connect") || looksLikeConnectFrame(originalBody)
	if useConnect {
		outType := "application/connect+proto"
		if strings.Contains(normalized, "json") {
			outType = firstNonEmptyContentType(contentType, outType)
		}
		return wrapConnectUnary(raw), outType, nil
	}
	return raw, firstNonEmptyContentType(contentType, "application/proto"), nil
}

func wrapConnectUnary(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func firstNonEmptyContentType(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "application/proto"
}
