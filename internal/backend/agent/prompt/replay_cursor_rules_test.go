package promptengine

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestBuildSelectedCursorRulesReplayMessageIncludesSlashSkillPathAndBody(t *testing.T) {
	t.Parallel()

	got, ok := BuildSelectedCursorRulesReplayMessage(&agentv1.UserMessage{
		Text: "/android-device 测试下这个能力，在模拟器上",
		SelectedContext: &agentv1.SelectedContext{
			CursorRules: []*agentv1.SelectedCursorRule{{
				Rule: &agentv1.CursorRule{
					FullPath: "/Users/gengu/.cursor/skills/android-device/SKILL.md",
					Content:  "# Android Device Control\npython3 scripts/touch.py <device_serial> tap 1 2",
				},
			}},
		},
	})
	if !ok {
		t.Fatal("expected slash-attached skill to produce a replay message")
	}
	if got.Role != "user" {
		t.Fatalf("role = %q", got.Role)
	}
	if !strings.Contains(got.Content, "<cursor_rules_context>") {
		t.Fatalf("missing cursor_rules_context wrapper: %s", got.Content)
	}
	if !strings.Contains(got.Content, `fullPath="/Users/gengu/.cursor/skills/android-device/SKILL.md"`) {
		t.Fatalf("missing authoritative fullPath: %s", got.Content)
	}
	if strings.Contains(got.Content, "skills-cursor/android-device") {
		t.Fatalf("must not invent skills-cursor path: %s", got.Content)
	}
	if !strings.Contains(got.Content, "# Android Device Control") {
		t.Fatalf("missing skill body: %s", got.Content)
	}
	if !strings.Contains(got.Content, "python3 scripts/touch.py <device_serial> tap 1 2") {
		t.Fatalf("skill-doc <> placeholders should stay literal: %s", got.Content)
	}
}

func TestBuildSelectedCursorRulesReplayMessageDedupesSelectedSkill(t *testing.T) {
	t.Parallel()

	path := "/Users/gengu/.cursor/skills/android-device/SKILL.md"
	got, ok := BuildSelectedCursorRulesReplayMessage(&agentv1.UserMessage{
		SelectedContext: &agentv1.SelectedContext{
			CursorRules: []*agentv1.SelectedCursorRule{{
				Rule: &agentv1.CursorRule{FullPath: path, Content: "from cursor rule"},
			}},
			SelectedSkills: []*agentv1.AgentSkill{{
				FullPath: path,
				Content:  "from selected skill",
			}},
		},
	})
	if !ok {
		t.Fatal("expected attached skill replay")
	}
	if strings.Count(got.Content, "<cursor_rule ") != 1 {
		t.Fatalf("same path should appear once: %s", got.Content)
	}
	if !strings.Contains(got.Content, "from cursor rule") {
		t.Fatalf("cursor_rules should win when listed first: %s", got.Content)
	}
}

func TestBuildSelectedCursorRulesReplayMessageEmpty(t *testing.T) {
	t.Parallel()

	if _, ok := BuildSelectedCursorRulesReplayMessage(nil); ok {
		t.Fatal("nil user message")
	}
	if _, ok := BuildSelectedCursorRulesReplayMessage(&agentv1.UserMessage{}); ok {
		t.Fatal("empty selected context")
	}
}
