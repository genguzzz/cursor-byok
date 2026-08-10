package forwarder

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestNormalizeUserMessageKeepsSlashSkillCursorRule(t *testing.T) {
	t.Parallel()

	path := "/Users/gengu/.cursor/skills/android-device/SKILL.md"
	body := strings.Repeat("a", 2000) + "\n# Android Device Control\n"
	got := normalizeUserMessageForStorage(&agentv1.UserMessage{
		Text: "/android-device test",
		SelectedContext: &agentv1.SelectedContext{
			CursorRules: []*agentv1.SelectedCursorRule{{
				Rule: &agentv1.CursorRule{FullPath: path, Content: body},
			}},
		},
	})
	rules := got.GetSelectedContext().GetCursorRules()
	if len(rules) != 1 || rules[0].GetRule() == nil {
		t.Fatalf("cursor rules dropped: %#v", got.GetSelectedContext())
	}
	if rules[0].GetRule().GetFullPath() != path {
		t.Fatalf("fullPath = %q", rules[0].GetRule().GetFullPath())
	}
	if !strings.Contains(rules[0].GetRule().GetContent(), "# Android Device Control") {
		t.Fatalf("skill body missing after guard: %q", rules[0].GetRule().GetContent())
	}
}

func TestBuildRunEntriesPersistsSelectedCursorRulesPromptContext(t *testing.T) {
	t.Parallel()

	entries, err := buildRunEntries(InboundIntent{
		RequestID: "req-slash-skill",
		UserMessage: &agentv1.UserMessage{
			Text: "/android-device 测试",
			SelectedContext: &agentv1.SelectedContext{
				CursorRules: []*agentv1.SelectedCursorRule{{
					Rule: &agentv1.CursorRule{
						FullPath: "/Users/gengu/.cursor/skills/android-device/SKILL.md",
						Content:  "# Android Device Control",
					},
				}},
			},
		},
	}, agentv1.AgentMode_AGENT_MODE_AGENT, 1)
	if err != nil {
		t.Fatalf("buildRunEntries: %v", err)
	}
	var found bool
	for _, entry := range entries {
		if entry.Kind != "prompt_context" {
			continue
		}
		payload := string(entry.Payload)
		if strings.Contains(payload, promptContextSourceSelectedCursorRules) &&
			strings.Contains(payload, "cursor_rules_context") &&
			strings.Contains(payload, "/Users/gengu/.cursor/skills/android-device/SKILL.md") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing selected_cursor_rules prompt_context: %#v", entries)
	}
}
