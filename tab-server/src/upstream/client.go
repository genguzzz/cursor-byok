// Package upstream talks to the CodeBuddy Chat Completions gateway.
package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/leookun/cursor-byok/tab-server/src/config"
)

// ErrEmptyUpstream is returned when the stream ends without producing content.
var ErrEmptyUpstream = errors.New("upstream 返回空内容")

// Client streams completions from the CodeBuddy gateway.
type Client struct {
	config config.UpstreamConfig
	token  string
	http   *http.Client
}

// NewClient builds a Client from configuration.
func NewClient(upstream config.UpstreamConfig, token string) *Client {
	timeout := time.Duration(upstream.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		config: upstream,
		token:  token,
		http:   &http.Client{Timeout: timeout},
	}
}

// Request is one Chat Completions call.
type Request struct {
	Messages []Message
	// StopSequences ends generation at the first occurrence.
	StopSequences []string
	MaxTokens     int
}

// Message is one Chat Completions message.
type Message struct {
	Role    string
	Content string
}

type chatRequest struct {
	Model      string      `json:"model"`
	Messages   []Message   `json:"messages"`
	Stream     bool        `json:"stream"`
	MaxTokens  int         `json:"max_tokens,omitempty"`
	Stop       []string    `json:"stop,omitempty"`
	Reasoning  string      `json:"reasoning_summary,omitempty"`
	ExtraBody  interface{} `json:"-"`
}

// Stream calls the gateway and forwards each text delta to emit. It returns
// the concatenated content.
func (c *Client) Stream(request Request, emit func(chunk string)) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model:     c.config.Model,
		Messages:  request.Messages,
		Stream:    true,
		MaxTokens: request.MaxTokens,
		Stop:      request.StopSequences,
		Reasoning: "auto",
	})
	if err != nil {
		return "", fmt.Errorf("序列化上游请求失败: %w", err)
	}
	// The CodeBuddy gateway rejects Accept-Encoding and expects a gzipped body.
	compressed, err := gzipBody(payload)
	if err != nil {
		return "", err
	}
	httpRequest, err := http.NewRequest(http.MethodPost, c.endpoint(), bytes.NewReader(compressed))
	if err != nil {
		return "", fmt.Errorf("构建上游请求失败: %w", err)
	}
	httpRequest.Header.Set("Content-Encoding", "gzip")
	c.applyHeaders(httpRequest)
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("上游请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return "", fmt.Errorf("上游返回 %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return readStream(response.Body, emit)
}

func readStream(body io.Reader, emit func(chunk string)) (string, error) {
	var builder strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		builder.WriteString(delta)
		if emit != nil {
			emit(delta)
		}
	}
	if err := scanner.Err(); err != nil {
		return builder.String(), fmt.Errorf("读取上游流失败: %w", err)
	}
	if builder.Len() == 0 {
		return "", ErrEmptyUpstream
	}
	return builder.String(), nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (c *Client) endpoint() string {
	return c.config.BaseURL + c.config.Endpoint
}

// applyHeaders sets the CLI-identifying header set the gateway requires,
// followed by the per-request identity/trace headers. Values absent from the
// configured map fall back to the CLI defaults this server mirrors, so an empty
// map still produces a working call. The user's own configured headers always
// win, and Accept-Encoding is never sent because the gateway rejects it.
func (c *Client) applyHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", bearer(c.token))
	identity := newCallIdentity()
	for name, value := range dynamicHeaders(c.token, identity) {
		request.Header.Set(name, value)
	}
	for _, header := range defaultHeaders {
		if _, exists := request.Header[http.CanonicalHeaderKey(header.name)]; !exists {
			request.Header.Set(header.name, header.value)
		}
	}
	for name, value := range c.config.Headers {
		request.Header.Set(name, value)
	}
	request.Header.Del("Accept-Encoding")
}

var defaultHeaders = []struct {
	name  string
	value string
}{
	{"x-requested-with", "XMLHttpRequest"},
	{"user-agent", "CLI/2.127.0 CodeBuddy/2.127.0"},
	{"x-ide-type", "CLI"},
	{"x-ide-name", "CLI"},
	{"x-ide-version", "2.127.0"},
	{"x-product", "SaaS"},
	{"x-agent-intent", "craft"},
	{"x-agent-purpose", "conversation"},
	{"x-private-data", "false"},
	{"x-enterprise-id", "etahzsqej0n4"},
	{"x-tenant-id", "etahzsqej0n4"},
	{"x-domain", "tencent.sso.copilot.tencent.com"},
	{"x-stainless-arch", "arm64"},
	{"x-stainless-lang", "js"},
	{"x-stainless-os", "MacOS"},
	{"x-stainless-package-version", "6.25.0"},
	{"x-stainless-runtime", "node"},
	{"x-stainless-runtime-version", "v23.11.1"},
	{"x-stainless-retry-count", "0"},
	{"x-codebuddy-request", "1"},
}

func bearer(token string) string {
	trimmed := strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		return trimmed
	}
	return "Bearer " + trimmed
}
