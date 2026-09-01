package tab

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leookun/cursor-byok/tab-server/src/config"
	"github.com/leookun/cursor-byok/tab-server/src/proto"
	"github.com/leookun/cursor-byok/tab-server/src/upstream"
)

// Handler owns the request decoders and the upstream client.
type Handler struct {
	service *Service
	client  *upstream.Client
	config  config.Config
}

// NewHandler builds a Handler.
func NewHandler(cfg config.Config) *Handler {
	client := upstream.NewClient(cfg.Upstream, cfg.Token)
	return &Handler{
		service: NewService(client, Config{
			MaxInputChars:      cfg.Tab.MaxInputChars,
			MaxOutputTokens:    cfg.Tab.MaxOutputTokens,
			EnableNextEdit:     cfg.Tab.EnableNextEdit,
			EnableGitCommitMsg: cfg.Tab.EnableGitCommitMsg,
		}, cfg.Upstream.Model),
		client: client,
		config: cfg,
	}
}

// WriteGitCommitMessage generates a commit message from staged diffs.
func (h *Handler) WriteGitCommitMessage(payload []byte) ([]byte, error) {
	if !h.config.Tab.EnableGitCommitMsg {
		return proto.EncodeWriteGitCommitMessageResponse(&proto.WriteGitCommitMessageResponse{}), nil
	}
	request, err := decodeWriteGitCommitMessageRequest(payload)
	if err != nil {
		return nil, err
	}
	prompt := buildGitCommitPrompt(request)
	content, err := h.client.Stream(upstream.Request{
		Messages: []upstream.Message{
			{Role: "system", Content: commitMessageSystemPrompt},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 200,
	}, nil)
	if err != nil {
		return nil, err
	}
	return proto.EncodeWriteGitCommitMessageResponse(&proto.WriteGitCommitMessageResponse{
		CommitMessage: cleanGeneratedText(content),
	}), nil
}

// WriteGitBranchName generates a branch name from a diff.
func (h *Handler) WriteGitBranchName(payload []byte) ([]byte, error) {
	if !h.config.Tab.EnableGitCommitMsg {
		return proto.EncodeWriteGitBranchNameResponse(&proto.WriteGitBranchNameResponse{}), nil
	}
	request, err := decodeWriteGitBranchNameRequest(payload)
	if err != nil {
		return nil, err
	}
	content, err := h.client.Stream(upstream.Request{
		Messages: []upstream.Message{
			{Role: "system", Content: branchNameSystemPrompt},
			{Role: "user", Content: buildGitBranchPrompt(request)},
		},
		MaxTokens: 60,
	}, nil)
	if err != nil {
		return nil, err
	}
	return proto.EncodeWriteGitBranchNameResponse(&proto.WriteGitBranchNameResponse{
		BranchName: sanitizeBranchName(content),
	}), nil
}

const commitMessageSystemPrompt = `You write git commit messages.
Given a diff, reply with a single conventional-commit subject line, at most 72 characters.
Use a prefix like feat, fix, refactor, docs, test or chore. Do not explain. Do not wrap in fences.`

const branchNameSystemPrompt = `You generate git branch names.
Given a change, reply with a single lowercase kebab-case branch name of at most five words.
Use no spaces, no slashes and no punctuation other than hyphens. Do not explain.`

// gitCommitMessageRequest is the subset of WriteGitCommitMessageRequest used here.
type gitCommitMessageRequest struct {
	Diffs                  []string
	PreviousCommitMessages []string
	ExplicitContext        string
}

// gitBranchNameRequest is the subset of WriteGitBranchNameRequest used here.
type gitBranchNameRequest struct {
	Diffs   string
	Context string
}

func decodeWriteGitCommitMessageRequest(payload []byte) (*gitCommitMessageRequest, error) {
	request := &gitCommitMessageRequest{}
	reader := newProtoReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return request, nil
		}
		switch field.Number {
		case 1:
			request.Diffs = append(request.Diffs, field.String())
		case 2:
			request.PreviousCommitMessages = append(request.PreviousCommitMessages, field.String())
		case 3:
			request.ExplicitContext = decodeExplicitContext(field.Bytes)
		}
	}
}

func decodeWriteGitBranchNameRequest(payload []byte) (*gitBranchNameRequest, error) {
	request := &gitBranchNameRequest{}
	reader := newProtoReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return request, nil
		}
		switch field.Number {
		case 1:
			request.Diffs = field.String()
		case 2:
			request.Context = field.String()
		}
	}
}

func decodeExplicitContext(payload []byte) string {
	var parts []string
	reader := newProtoReader(payload)
	for {
		field, ok, err := reader.Next()
		if err != nil || !ok {
			break
		}
		if field.Number == 1 {
			parts = append(parts, field.String())
		}
	}
	return strings.Join(parts, " ")
}

func buildGitCommitPrompt(request *gitCommitMessageRequest) string {
	var builder strings.Builder
	builder.WriteString("Diffs:\n")
	for _, diff := range request.Diffs {
		builder.WriteString(diff)
		builder.WriteString("\n")
	}
	if len(request.PreviousCommitMessages) > 0 {
		builder.WriteString("\nRecent commit messages for style:\n")
		for _, message := range request.PreviousCommitMessages {
			builder.WriteString(message)
			builder.WriteString("\n")
		}
	}
	if strings.TrimSpace(request.ExplicitContext) != "" {
		builder.WriteString("\nAdditional context:\n")
		builder.WriteString(request.ExplicitContext)
		builder.WriteString("\n")
	}
	return truncate(builder.String(), 12000)
}

func buildGitBranchPrompt(request *gitBranchNameRequest) string {
	var builder strings.Builder
	builder.WriteString("Change:\n")
	builder.WriteString(request.Diffs)
	if strings.TrimSpace(request.Context) != "" {
		builder.WriteString("\nContext:\n")
		builder.WriteString(request.Context)
	}
	builder.WriteString("\n")
	return truncate(builder.String(), 12000)
}

// cleanGeneratedText strips markdown fences and collapses the model reply to
// its first meaningful line.
func cleanGeneratedText(content string) string {
	value := strings.TrimSpace(content)
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "```" {
			continue
		}
		return trimmed
	}
	return value
}

// sanitizeBranchName converts free-form text into a valid git ref component.
func sanitizeBranchName(content string) string {
	value := strings.ToLower(cleanGeneratedText(content))
	var builder strings.Builder
	lastHyphen := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		case r == ' ' || r == '_' || r == '/' || r == '-' || r == '.':
			if !lastHyphen && builder.Len() > 0 {
				builder.WriteRune('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

// decodeJSONError surfaces an upstream JSON error body.
func decodeJSONError(body string) error {
	var payload struct {
		Message string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err == nil && payload.Message != "" {
		return errors.New(payload.Message)
	}
	return errors.New(strings.TrimSpace(body))
}

// unsupported reports a method this server does not implement.
func unsupported(method string) error {
	return fmt.Errorf("%s 未实现", method)
}
