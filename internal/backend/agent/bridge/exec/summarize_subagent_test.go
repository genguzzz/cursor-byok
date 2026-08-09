package execbridge

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestSummarizeSubagentResultBackgroundIncludesMentionHint(t *testing.T) {
	t.Parallel()

	result := &agentv1.SubagentResult{
		Result: &agentv1.SubagentResult_Success{
			Success: &agentv1.SubagentSuccess{
				AgentId:           "cd29cadd-9527-4af0-8eaf-f4b99a6933bf",
				BackgroundReason:  agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_AGENT_REQUEST,
				TranscriptPath:    stringPtr("/tmp/transcript.jsonl"),
			},
		},
	}
	got := summarizeSubagentResult([]byte(`{"description":"调研代码架构","prompt":"x","subagent_type":"explore"}`), result)
	if !strings.Contains(got, "subagent running in background agent_id=cd29cadd-9527-4af0-8eaf-f4b99a6933bf") {
		t.Fatalf("missing background summary: %s", got)
	}
	if !strings.Contains(got, "When mentioning this subagent to the user, write exactly: [调研代码架构](cd29cadd-9527-4af0-8eaf-f4b99a6933bf)") {
		t.Fatalf("missing mention hint: %s", got)
	}
}

func TestSummarizeSubagentResultSanitizesLabelBrackets(t *testing.T) {
	t.Parallel()

	got := subagentUserMentionHint([]byte(`{"description":"[bad] label"}`), "abc-id")
	if strings.Contains(got, "[[") || strings.Contains(got, "]]") {
		t.Fatalf("label still contains brackets: %s", got)
	}
	if !strings.Contains(got, "[bad label](abc-id)") {
		t.Fatalf("unexpected hint: %s", got)
	}
}
