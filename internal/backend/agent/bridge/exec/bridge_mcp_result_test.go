package execbridge

import (
	"fmt"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestConvertMcpResultServerNotFoundIncludesAvailableServers(t *testing.T) {
	t.Parallel()

	got := convertMcpResult(&agentv1.McpResult{
		Result: &agentv1.McpResult_ServerNotFound{
			ServerNotFound: &agentv1.McpServerNotFound{
				Name: "chrome",
				AvailableServers: []string{
					"cursor-ide-browser",
					"user-chrome-devtools",
					"user-sequential-thinking",
				},
			},
		},
	})
	text := summarizeMcpResult(got)
	if !strings.Contains(text, "mcp server not found: chrome") {
		t.Fatalf("missing server name: %s", text)
	}
	if !strings.Contains(text, "available servers (") {
		t.Fatalf("missing available servers header: %s", text)
	}
	if !strings.Contains(text, "user-chrome-devtools") {
		t.Fatalf("dropped available server list: %s", text)
	}
	if got.GetError() == nil {
		t.Fatal("server_not_found should map to McpToolResult error")
	}
}

func TestConvertMcpResultToolNotFoundKeepsLookupNameOnly(t *testing.T) {
	t.Parallel()

	got := convertMcpResult(&agentv1.McpResult{
		Result: &agentv1.McpResult_ToolNotFound{
			ToolNotFound: &agentv1.McpToolNotFound{
				Name: "user-tapd_mcp_http-bugs_get",
				AvailableTools: []string{
					"cursor-ide-browser-browser_navigate",
					"user-tapd_mcp_http-lookup_tapd_tool",
					"user-tapd_mcp_http-proxy_execute_tool",
				},
			},
		},
	})
	text := summarizeMcpResult(got)
	if text != "tool not found: user-tapd_mcp_http-bugs_get" {
		t.Fatalf("should forward client lookup name only: %s", text)
	}
}

func TestConvertMcpResultToolNotFoundDoesNotDumpCatalog(t *testing.T) {
	t.Parallel()

	tools := make([]string, 0, 40)
	for i := 0; i < 30; i++ {
		tools = append(tools, fmt.Sprintf("cursor-ide-browser-browser_extra_%d", i))
	}
	tools = append(tools, "user-tapd_mcp_http-lookup_tapd_tool", "user-knot-knowledgebase_search")
	got := convertMcpResult(&agentv1.McpResult{
		Result: &agentv1.McpResult_ToolNotFound{
			ToolNotFound: &agentv1.McpToolNotFound{
				Name:           "user-tapd_mcp_http-bugs_get",
				AvailableTools: tools,
			},
		},
	})
	text := summarizeMcpResult(got)
	if strings.Contains(text, "closest tools:") || strings.Contains(text, "available MCP servers") || strings.Contains(text, "available tools:") {
		t.Fatalf("must not invent catalog from available_tools: %s", text)
	}
	if strings.Contains(text, "browser_extra_0") || strings.Contains(text, "lookup_tapd_tool") {
		t.Fatalf("catalog leaked into model-visible error: %s", text)
	}
}

func TestConvertMcpResultToolNotFoundWithoutAvailableTools(t *testing.T) {
	t.Parallel()

	got := convertMcpResult(&agentv1.McpResult{
		Result: &agentv1.McpResult_ToolNotFound{
			ToolNotFound: &agentv1.McpToolNotFound{Name: "missing-tool"},
		},
	})
	text := summarizeMcpResult(got)
	if text != "tool not found: missing-tool" {
		t.Fatalf("unexpected text: %s", text)
	}
}

func TestConvertMcpResultNilAndEmpty(t *testing.T) {
	t.Parallel()

	if text := summarizeMcpResult(convertMcpResult(nil)); text != "mcp result missing" {
		t.Fatalf("nil result: %s", text)
	}
	if text := summarizeMcpResult(convertMcpResult(&agentv1.McpResult{})); text != "unknown mcp result: empty" {
		t.Fatalf("empty oneof: %s", text)
	}
}

func TestApplyExecClientMessageMCPServerNotFoundIsTerminal(t *testing.T) {
	t.Parallel()

	bridge := NewBridge()
	applied, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     1,
		ExecId: "exec-mcp-1",
		Message: &agentv1.ExecClientMessage_McpResult{
			McpResult: &agentv1.McpResult{
				Result: &agentv1.McpResult_ServerNotFound{
					ServerNotFound: &agentv1.McpServerNotFound{
						Name:             "chrome",
						AvailableServers: []string{"user-chrome-devtools"},
					},
				},
			},
		},
	}, runtimecore.PendingExec{
		ToolCallID: "tc_mcp_1",
		ExecID:     "exec-mcp-1",
		ExecKind:   "mcp",
		ArgsJSON:   []byte(`{"server":"chrome","toolName":"list_pages"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage: %v", err)
	}
	if !applied.IsTerminal {
		t.Fatal("server_not_found should complete the MCP exec")
	}
	if !strings.Contains(applied.ToolResultPayload, "user-chrome-devtools") {
		t.Fatalf("model-visible payload dropped available servers: %s", applied.ToolResultPayload)
	}
	if applied.ToolCall == nil || applied.ToolCall.GetMcpToolCall().GetResult().GetError() == nil {
		t.Fatal("expected completed MCP tool call with error result")
	}
}

func TestApplyExecClientMessageMCPApprovedIsNotTerminal(t *testing.T) {
	t.Parallel()

	bridge := NewBridge()
	applied, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     2,
		ExecId: "exec-mcp-2",
		Message: &agentv1.ExecClientMessage_McpResult{
			McpResult: &agentv1.McpResult{
				Result: &agentv1.McpResult_Approved{
					Approved: &agentv1.McpApproved{},
				},
			},
		},
	}, runtimecore.PendingExec{
		ToolCallID: "tc_mcp_2",
		ExecID:     "exec-mcp-2",
		ExecKind:   "mcp",
		ArgsJSON:   []byte(`{"server":"user-chrome-devtools","toolName":"list_pages"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage: %v", err)
	}
	if applied.IsTerminal {
		t.Fatal("approved should wait for the later success/error result")
	}
	if applied.ToolResultPayload != "" {
		t.Fatalf("approved should not invent a tool result: %s", applied.ToolResultPayload)
	}
	if applied.ToolCall != nil {
		t.Fatal("approved should not complete the MCP tool call")
	}
}

func TestApplyExecClientMessageMCPToolNotFoundKeepsLookupName(t *testing.T) {
	t.Parallel()

	bridge := NewBridge()
	applied, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     3,
		ExecId: "exec-mcp-3",
		Message: &agentv1.ExecClientMessage_McpResult{
			McpResult: &agentv1.McpResult{
				Result: &agentv1.McpResult_ToolNotFound{
					ToolNotFound: &agentv1.McpToolNotFound{
						Name:           "chrome-devtools-list_pages",
						AvailableTools: []string{"user-chrome-devtools-list_pages"},
					},
				},
			},
		},
	}, runtimecore.PendingExec{
		ToolCallID: "tc_mcp_3",
		ExecID:     "exec-mcp-3",
		ExecKind:   "mcp",
		ArgsJSON:   []byte(`{"server":"chrome-devtools","toolName":"list_pages"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecClientMessage: %v", err)
	}
	if !applied.IsTerminal {
		t.Fatal("tool_not_found should complete the MCP exec")
	}
	if !strings.Contains(applied.ToolResultPayload, "tool not found: chrome-devtools-list_pages") {
		t.Fatalf("model-visible payload dropped lookup name: %s", applied.ToolResultPayload)
	}
	if strings.Contains(applied.ToolResultPayload, "user-chrome-devtools-list_pages") {
		t.Fatalf("must not dump client available_tools into model context: %s", applied.ToolResultPayload)
	}
}
