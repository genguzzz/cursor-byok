package prompt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadPromptUsesModePromptFiles(t *testing.T) {
	cases := []struct {
		mode   Mode
		opener string
	}{
		{ModeAgent, "You are a powerful agentic AI coding assistant powered by Cursor"},
		{ModeAsk, "You are an AI coding assistant, powered by"},
		{ModePlan, "You are a powerful agentic AI coding assistant powered by Cursor"},
		{ModeMultitask, "You are a powerful agentic AI coding assistant powered by Cursor"},
		{ModeDebug, "You are a debugging specialist operating in DEBUG MODE"},
	}
	for _, tc := range cases {
		t.Run(string(tc.mode), func(t *testing.T) {
			text, err := ReadPrompt(tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "极度务实") || strings.Contains(text, "你是 Cursor IDE") {
				t.Fatal("mode prompt must be traffic-aligned English, not legacy Chinese overlay")
			}
			if !strings.Contains(text, tc.opener) {
				t.Fatalf("missing mode opener %q", tc.opener)
			}
			chars := utf8.RuneCountInString(strings.TrimSpace(text))
			if chars < 1200 || chars > 2800 {
				t.Fatalf("system prompt char count=%d outside ~1877 traffic band", chars)
			}
		})
	}
}

func TestReadPlanSystemReminderMatchesOfficialContract(t *testing.T) {
	text, err := ReadPlanSystemReminder()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Plan mode is active, unless you have already seen the <end_plan_mode/> tag") {
		t.Fatal("plan reminder must use official Plan mode is active opener")
	}
	if !strings.Contains(text, "CreatePlan") {
		t.Fatal("plan reminder must name CreatePlan")
	}
}
