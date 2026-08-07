package forwarder

import (
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestShellToolCallDeltaFromOutputUpdate(t *testing.T) {
	t.Parallel()

	stdoutDelta := &agentv1.ShellOutputDeltaUpdate{
		Event: &agentv1.ShellOutputDeltaUpdate_Stdout{
			Stdout: &agentv1.ShellStreamStdout{Data: "hello\n"},
		},
	}
	got := shellToolCallDeltaFromOutputUpdate(stdoutDelta)
	if got == nil || got.GetShellToolCallDelta() == nil || got.GetShellToolCallDelta().GetStdout() == nil {
		t.Fatalf("expected stdout tool call delta, got %#v", got)
	}
	if got.GetShellToolCallDelta().GetStdout().GetContent() != "hello\n" {
		t.Fatalf("unexpected stdout content: %q", got.GetShellToolCallDelta().GetStdout().GetContent())
	}

	stderrDelta := &agentv1.ShellOutputDeltaUpdate{
		Event: &agentv1.ShellOutputDeltaUpdate_Stderr{
			Stderr: &agentv1.ShellStreamStderr{Data: "warn\n"},
		},
	}
	got = shellToolCallDeltaFromOutputUpdate(stderrDelta)
	if got == nil || got.GetShellToolCallDelta() == nil || got.GetShellToolCallDelta().GetStderr() == nil {
		t.Fatalf("expected stderr tool call delta, got %#v", got)
	}
	if got.GetShellToolCallDelta().GetStderr().GetContent() != "warn\n" {
		t.Fatalf("unexpected stderr content: %q", got.GetShellToolCallDelta().GetStderr().GetContent())
	}

	if shellToolCallDeltaFromOutputUpdate(&agentv1.ShellOutputDeltaUpdate{
		Event: &agentv1.ShellOutputDeltaUpdate_Exit{Exit: &agentv1.ShellStreamExit{Code: 0}},
	}) != nil {
		t.Fatal("exit events should not produce tool call delta")
	}
}

func TestIsTransientReplaySafeEventKeepsShellStreaming(t *testing.T) {
	t.Parallel()

	shellOutput := buildShellOutputDeltaMessage(&agentv1.ShellOutputDeltaUpdate{
		Event: &agentv1.ShellOutputDeltaUpdate_Stdout{
			Stdout: &agentv1.ShellStreamStdout{Data: "chunk"},
		},
	})
	if isTransientReplaySafeEvent(shellOutput) {
		t.Fatal("shell_output_delta must be replayed")
	}

	toolCallDelta := buildToolCallDeltaMessage("tc_1", "mc_1", &agentv1.ToolCallDelta{
		Delta: &agentv1.ToolCallDelta_ShellToolCallDelta{
			ShellToolCallDelta: &agentv1.ShellToolCallDelta{
				Delta: &agentv1.ShellToolCallDelta_Stdout{
					Stdout: &agentv1.ShellToolCallStdoutDelta{Content: "chunk"},
				},
			},
		},
	})
	if isTransientReplaySafeEvent(toolCallDelta) {
		t.Fatal("shell tool_call_delta must be replayed")
	}

	shellStarted := buildToolCallStartedMessage("tc_1", "mc_1", &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{Command: "echo hi", ToolCallId: "tc_1"},
			},
		},
	})
	if isTransientReplaySafeEvent(shellStarted) {
		t.Fatal("shell tool_call_started must be replayed")
	}

	thinking := buildThinkingDeltaMessage("thinking...", agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT)
	if !isTransientReplaySafeEvent(thinking) {
		t.Fatal("thinking delta should be skipped on reconnect")
	}
}

func TestTakeBackgroundShellUIStdoutDelta(t *testing.T) {
	t.Parallel()

	state := &BackgroundShellState{StdoutBuffer: "line1\nline2\n"}
	if got := takeBackgroundShellUIStdoutDeltaLocked(state); got != "line1\nline2\n" {
		t.Fatalf("first delta = %q", got)
	}
	if state.UIStdoutOffset != len(state.StdoutBuffer) {
		t.Fatalf("offset = %d", state.UIStdoutOffset)
	}
	state.StdoutBuffer += "line3\n"
	if got := takeBackgroundShellUIStdoutDeltaLocked(state); got != "line3\n" {
		t.Fatalf("incremental delta = %q", got)
	}
	if got := takeBackgroundShellUIStdoutDeltaLocked(state); got != "" {
		t.Fatalf("empty delta expected, got %q", got)
	}
}

func TestMergeBackgroundShellStdoutFromTerminalSnapshot(t *testing.T) {
	t.Parallel()

	state := &BackgroundShellState{}
	mergeBackgroundShellStdoutFromTerminalOutput(state, "a\nb\n")
	if state.StdoutBuffer != "a\nb\n" {
		t.Fatalf("initial merge = %q", state.StdoutBuffer)
	}
	mergeBackgroundShellStdoutFromTerminalOutput(state, "a\nb\nc\n")
	if state.StdoutBuffer != "a\nb\nc\n" {
		t.Fatalf("growth merge = %q", state.StdoutBuffer)
	}
	if delta := takeBackgroundShellUIStdoutDeltaLocked(state); delta != "a\nb\nc\n" {
		t.Fatalf("ui delta after growth = %q", delta)
	}
}

func TestBackgroundShellIDFromToolCall(t *testing.T) {
	t.Parallel()

	shellID := uint32(587982)
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Result: &agentv1.ShellResult{
					IsBackground: boolPtr(true),
					Result: &agentv1.ShellResult_Success{
						Success: &agentv1.ShellSuccess{ShellId: &shellID},
					},
				},
			},
		},
	}
	if got := backgroundShellIDFromToolCall(toolCall); got != "587982" {
		t.Fatalf("shell id = %q", got)
	}
}

func TestBackgroundedMarksUIStartedEnsured(t *testing.T) {
	t.Parallel()

	stream := &ActiveStream{
		BackgroundShells:           map[string]*BackgroundShellState{},
		BackgroundShellsByMessageID: map[uint32]string{},
		BackgroundShellsByExecID:   map[string]string{},
	}
	pending := runtimecore.PendingExec{
		ToolCallID:            "tc_bg",
		ExecID:                "exec-1",
		MessageID:             7,
		ShellUIStartedEnsured: false,
	}
	now := time.Now().UTC()
	service := &Service{}
	service.observeShellStreamLocked(stream, pending, &agentv1.ShellStream{
		Event: &agentv1.ShellStream_Backgrounded{
			Backgrounded: &agentv1.ShellStreamBackgrounded{
				ShellId: 42,
				Command: "echo hi",
			},
		},
	}, now)

	state := stream.BackgroundShells["42"]
	if state == nil {
		t.Fatal("missing background state")
	}
	if !state.UIStartedEnsured {
		t.Fatal("backgrounded must mark UIStartedEnsured to avoid re-started after completed")
	}
}

func boolPtr(v bool) *bool { return &v }

func TestChunkShellStreamText(t *testing.T) {
	t.Parallel()

	if got := chunkShellStreamText("abc", 10); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("unexpected small chunk result: %#v", got)
	}

	input := strings.Repeat("a", 10) + "\n" + strings.Repeat("b", 10)
	got := chunkShellStreamText(input, 12)
	if len(got) < 2 {
		t.Fatalf("expected split chunks, got %#v", got)
	}
	var joined strings.Builder
	for _, chunk := range got {
		if chunk == "" {
			t.Fatal("empty chunk")
		}
		joined.WriteString(chunk)
	}
	if joined.String() != input {
		t.Fatalf("chunks lost data: %q != %q", joined.String(), input)
	}
}
