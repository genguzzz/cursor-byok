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
	if encoding != "gzip" && !(encoding == "" && body[0] == 0x1f && body[1] == 0x8b) {
		return body
	}
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
	if flags&0x01 == 0 {
		return payload, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxConnectFrameBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
	}
	if len(decoded) > maxConnectFrameBytes {
		return nil, fmt.Errorf("gzip payload too large")
	}
	return decoded, nil
}
