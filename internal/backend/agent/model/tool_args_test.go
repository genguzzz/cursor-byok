package modeladapter

import (
	"strings"
	"testing"
)

func TestCompletedToolArgsJSON(t *testing.T) {
	t.Run("empty becomes object", func(t *testing.T) {
		got, err := completedToolArgsJSON("Shell", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if string(got) != "{}" {
			t.Fatalf("got %s, want {}", got)
		}
	})

	t.Run("valid object kept", func(t *testing.T) {
		raw := `{"command":"ls"}`
		got, err := completedToolArgsJSON("Shell", raw)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if string(got) != raw {
			t.Fatalf("got %s, want %s", got, raw)
		}
	})

	t.Run("truncated json rejected", func(t *testing.T) {
		_, err := completedToolArgsJSON("Shell", `{"command":"echo`)
		if err == nil {
			t.Fatal("expected error for truncated json")
		}
		if !strings.Contains(err.Error(), "Shell") {
			t.Fatalf("error should mention tool name, got %v", err)
		}
	})

	t.Run("non object rejected", func(t *testing.T) {
		_, err := completedToolArgsJSON("Shell", `["a"]`)
		if err == nil {
			t.Fatal("expected error for non-object json")
		}
	})
}

func TestIsValidProviderToolArgumentsJSON(t *testing.T) {
	if !isValidProviderToolArgumentsJSON(`{"ok":true}`) {
		t.Fatal("valid object should pass")
	}
	if isValidProviderToolArgumentsJSON(`{"ok":`) {
		t.Fatal("truncated json should fail")
	}
}

func TestRepairInvalidProviderToolCallArguments(t *testing.T) {
	input := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{
				{Type: "function", Function: ToolCallFunctionShape{Name: "Shell", Arguments: `{"command":"echo`}},
				{Type: "function", Function: ToolCallFunctionShape{Name: "Read", Arguments: `{"path":"a.go"}`}},
			},
		},
		{Role: "tool", ToolCallID: "1", Content: "ok"},
	}
	got := repairInvalidProviderToolCallArguments(input)
	if got[0].ToolCalls[0].Function.Arguments != "{}" {
		t.Fatalf("invalid args should repair to {}, got %q", got[0].ToolCalls[0].Function.Arguments)
	}
	if got[0].ToolCalls[1].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("valid args should keep, got %q", got[0].ToolCalls[1].Function.Arguments)
	}
}

func TestCanMergeProviderAssistantTextWithToolCalls(t *testing.T) {
	last := Message{Role: "assistant", Content: "接下来搜索"}
	current := Message{
		Role: "assistant",
		ToolCalls: []ToolCallDescriptor{
			{Type: "function", Function: ToolCallFunctionShape{Name: "Shell", Arguments: `{}`}},
		},
	}
	if !canMergeProviderAssistantTextWithToolCalls(last, current) {
		t.Fatal("text + tool_calls should merge")
	}

	messages := []Message{last}
	if !mergeProviderAssistantToolCalls(&messages, current) {
		t.Fatal("merge should succeed")
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after merge, got %d", len(messages))
	}
	if strings.TrimSpace(messages[0].Content) != "接下来搜索" {
		t.Fatalf("content lost: %q", messages[0].Content)
	}
	if len(messages[0].ToolCalls) != 1 {
		t.Fatalf("tool calls not merged: %+v", messages[0].ToolCalls)
	}
}

func TestCanMergeProviderAssistantToolCallsOnly(t *testing.T) {
	last := Message{
		Role: "assistant",
		ToolCalls: []ToolCallDescriptor{
			{Type: "function", Function: ToolCallFunctionShape{Name: "Read", Arguments: `{}`}},
		},
	}
	current := Message{
		Role: "assistant",
		ToolCalls: []ToolCallDescriptor{
			{Type: "function", Function: ToolCallFunctionShape{Name: "Shell", Arguments: `{}`}},
		},
	}
	if !canMergeProviderAssistantToolCalls(last, current) {
		t.Fatal("tool + tool should merge")
	}
	if canMergeProviderAssistantTextWithToolCalls(last, current) {
		t.Fatal("tool + tool should not match text-merge helper")
	}
}
