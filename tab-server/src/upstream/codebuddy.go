// Package upstream talks to the CodeBuddy Chat Completions gateway.
//
// CodeBuddy is not a distinct wire protocol: its gateway accepts OpenAI Chat
// Completions bodies but requires a large, exact set of CLI-identifying headers
// plus per-request identity/trace headers, and it rejects Accept-Encoding while
// expecting a gzipped request body. This file mirrors the cursor-byok Rust
// server's codebuddy transport decoration (server/src/provider/codebuddy.rs) so
// both surfaces stay in sync.
package upstream

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// callIdentity carries the per-request identity and trace ids. The CodeBuddy
// gateway correlates them, so they are generated fresh for every call; the Tab
// surface has no long-lived conversation to reuse.
type callIdentity struct {
	conversationID        string
	conversationRequestID string
	messageID             string
	traceID               string
	spanID                string
	parentSpanID          string
}

// newCallIdentity mints a fresh identity. The conversation id keeps the dash
// form of a UUID while the request/message ids are dash-free hex, matching the
// Rust CallIdentity layout.
func newCallIdentity() callIdentity {
	return callIdentity{
		conversationID:        newUUID(),
		conversationRequestID: randomHex(16),
		messageID:             randomHex(16),
		traceID:               randomHex(16),
		spanID:                randomHex(8),
		parentSpanID:          randomHex(8),
	}
}

// randomHex returns n random bytes hex-encoded. A trace id is 16 bytes, a span
// id 8, mirroring the Rust side.
func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand only fails when the OS entropy source is broken; a
		// completion cannot proceed meaningfully without it.
		panic(fmt.Sprintf("随机源不可用: %v", err))
	}
	return hex.EncodeToString(buf)
}

// newUUID returns a version 4 UUID string (dash form) built from crypto/rand.
func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("随机源不可用: %v", err))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// dynamicHeaders returns the per-request identity and trace headers the
// gateway requires. A configured header always wins later in applyHeaders, so
// this only fills gaps.
func dynamicHeaders(token string, identity callIdentity) map[string]string {
	headers := map[string]string{
		"x-conversation-id":         identity.conversationID,
		"x-conversation-request-id": identity.conversationRequestID,
		"x-request-id":              identity.messageID,
		"x-conversation-message-id": identity.messageID,
		"x-root-request-id":         identity.conversationRequestID,
		"x-b3-traceid":              identity.traceID,
		"x-b3-spanid":               identity.spanID,
		"x-b3-parentspanid":         identity.parentSpanID,
		"x-b3-sampled":              "1",
		"x-trace-id":                identity.traceID,
		"traceparent":               fmt.Sprintf("00-%s-%s-01", identity.traceID, identity.spanID),
		"b3":                        fmt.Sprintf("%s-%s-1-%s", identity.traceID, identity.spanID, identity.parentSpanID),
	}
	if userID := userIDFromToken(token); userID != "" {
		headers["x-user-id"] = userID
	}
	return headers
}

// userIDFromToken extracts the JWT `sub` claim from the bearer token. The
// gateway expects X-User-Id to match it; a non-JWT key simply omits the header.
func userIDFromToken(token string) string {
	trimmed := strings.TrimSpace(token)
	trimmed = strings.TrimPrefix(trimmed, "Bearer ")
	trimmed = strings.TrimPrefix(trimmed, "bearer ")
	parts := strings.Split(trimmed, ".")
	if len(parts) < 2 {
		return ""
	}
	payload := strings.TrimRight(parts[1], "=")
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		if decoded, err = base64.URLEncoding.DecodeString(payload); err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

// gzipBody compresses the request body. Paired with suppressing Accept-Encoding
// (and the transport's automatic compression), this matches what the CodeBuddy
// CLI puts on the wire.
func gzipBody(body []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		return nil, fmt.Errorf("gzip 写入失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("gzip 关闭失败: %w", err)
	}
	return buffer.Bytes(), nil
}
