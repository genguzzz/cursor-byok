// Package tab implements the Cursor Tab completion surface on top of a
// Chat Completions upstream.
package tab

import (
	"fmt"
	"strings"

	"github.com/leookun/cursor-byok/tab-server/src/proto"
	"github.com/leookun/cursor-byok/tab-server/src/upstream"
)

// Service answers Tab requests by prompting the configured model.
type Service struct {
	client    *upstream.Client
	config    Config
	modelName string
}

// Config tunes prompt construction and generation limits.
type Config struct {
	MaxInputChars      int
	MaxOutputTokens    int
	EnableNextEdit     bool
	EnableGitCommitMsg bool
}

// NewService builds a Service.
func NewService(client *upstream.Client, config Config, modelName string) *Service {
	return &Service{client: client, config: config, modelName: modelName}
}

// Complete streams an inline completion at the cursor. Each text delta is
// forwarded to emit as it arrives.
func (s *Service) Complete(request *proto.StreamCppRequest, emit func(chunk string)) (string, error) {
	if request == nil || request.CurrentFile == nil {
		return "", fmt.Errorf("请求缺少 current_file")
	}
	prompt, err := s.buildCompletionPrompt(request)
	if err != nil {
		return "", err
	}
	return s.client.Stream(upstream.Request{
		Messages: []upstream.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		MaxTokens:     s.config.MaxOutputTokens,
		StopSequences: []string{"\n\n\n", "<|END|>"},
	}, emit)
}

// NextEdit locates the next place the user is likely to edit.
func (s *Service) NextEdit(request *proto.StreamNextCursorPredictionRequest) (*proto.StreamNextCursorPredictionResponse, error) {
	if !s.config.EnableNextEdit {
		notInRange := true
		return &proto.StreamNextCursorPredictionResponse{IsNotInRange: &notInRange}, nil
	}
	if request == nil || request.CurrentFile == nil {
		return nil, fmt.Errorf("请求缺少 current_file")
	}
	prompt := s.buildNextEditPrompt(request)
	content, err := s.client.Stream(upstream.Request{
		Messages: []upstream.Message{
			{Role: "system", Content: nextEditSystemPrompt},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 64,
	}, nil)
	if err != nil {
		return nil, err
	}
	return parseNextEdit(content), nil
}

func parseNextEdit(content string) *proto.StreamNextCursorPredictionResponse {
	line := firstLineNumber(strings.TrimSpace(content))
	if line <= 0 {
		notInRange := true
		return &proto.StreamNextCursorPredictionResponse{IsNotInRange: &notInRange}
	}
	return &proto.StreamNextCursorPredictionResponse{LineNumber: &line}
}

const systemPrompt = `You are a code completion engine. Continue the code at the cursor.
Reply with only the missing code that should be inserted at the cursor position.
Do not repeat code that already appears before or after the cursor.
Do not wrap the answer in markdown fences. Do not explain. If there is nothing useful to add, reply with an empty string.`

const nextEditSystemPrompt = `You predict the next place a developer will edit.
Given a file and the cursor position, reply with only the 1-indexed line number of the next likely edit.
Reply with a single integer and nothing else. If no edit is likely, reply with 0.`

// buildCompletionPrompt renders the file around the cursor for the model.
func (s *Service) buildCompletionPrompt(request *proto.StreamCppRequest) (string, error) {
	file := request.CurrentFile
	lines := strings.Split(file.Contents, "\n")
	cursorLine := 0
	cursorColumn := 0
	if file.CursorPosition != nil {
		cursorLine = int(file.CursorPosition.Line)
		cursorColumn = int(file.CursorPosition.Column)
	}
	if cursorLine < 0 {
		cursorLine = 0
	}
	if cursorLine > len(lines) {
		cursorLine = len(lines)
	}
	if cursorColumn < 0 {
		cursorColumn = 0
	}
	before := joinLines(lines[:min(cursorLine, len(lines))])
	cursorLineText := ""
	if cursorLine < len(lines) {
		cursorLineText = lines[cursorLine]
	}
	if cursorColumn > len(cursorLineText) {
		cursorColumn = len(cursorLineText)
	}
	prefix := cursorLineText[:cursorColumn]
	suffix := cursorLineText[cursorColumn:]
	after := joinLines(lines[min(cursorLine+1, len(lines)):])

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("File: %s (language: %s, cursor at line %d column %d)\n\n",
		orUnknown(file.RelativeWorkspacePath), orUnknown(file.LanguageID), cursorLine+1, cursorColumn))
	builder.WriteString("```\n")
	builder.WriteString(before)
	if before != "" {
		builder.WriteString("\n")
	}
	builder.WriteString(prefix)
	builder.WriteString("<<<CURSOR>>>")
	builder.WriteString(suffix)
	if after != "" {
		builder.WriteString("\n")
		builder.WriteString(after)
	}
	builder.WriteString("\n```\n")
	for _, item := range request.ContextItems {
		if item == nil || item.Contents == "" {
			continue
		}
		label := item.RelativeWorkspacePath
		if label == "" && item.Symbol != nil {
			label = *item.Symbol
		}
		builder.WriteString(fmt.Sprintf("\nRelated (%s):\n```\n%s\n```\n", orUnknown(label), item.Contents))
	}
	if len(request.DiffHistory) > 0 {
		builder.WriteString("\nRecent edits:\n")
		for _, diff := range request.DiffHistory {
			builder.WriteString(diff)
			builder.WriteString("\n")
		}
	}
	return truncate(builder.String(), s.config.MaxInputChars), nil
}

func (s *Service) buildNextEditPrompt(request *proto.StreamNextCursorPredictionRequest) string {
	file := request.CurrentFile
	cursorLine := 0
	if file.CursorPosition != nil {
		cursorLine = int(file.CursorPosition.Line) + 1
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("File: %s\nCursor is at line %d.\n\n```\n%s\n```\n",
		orUnknown(file.RelativeWorkspacePath), cursorLine, file.Contents))
	if len(request.DiffHistory) > 0 {
		builder.WriteString("\nRecent edits:\n")
		for _, diff := range request.DiffHistory {
			builder.WriteString(diff)
			builder.WriteString("\n")
		}
	}
	return truncate(builder.String(), s.config.MaxInputChars)
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
