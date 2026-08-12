package runtimecore

import (
	"encoding/json"
	"testing"
)

func TestDecodeMCPToolPayloadAcceptsOfficialCamelCase(t *testing.T) {
	t.Parallel()

	got, err := DecodeMCPToolPayload([]byte(`{"server":"user-tapd_mcp_http","toolName":"lookup_tapd_tool","arguments":{"task_description":"查询缺陷"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "user-tapd_mcp_http" || got.ToolName != "lookup_tapd_tool" {
		t.Fatalf("server/tool: %+v", got)
	}
	if got.Arguments["task_description"] != "查询缺陷" {
		t.Fatalf("arguments: %+v", got.Arguments)
	}
}

func TestDecodeMCPToolPayloadAcceptsSnakeCaseProxyShape(t *testing.T) {
	t.Parallel()

	got, err := DecodeMCPToolPayload([]byte(`{"server":"user-tapd_mcp_http","tool_name":"proxy_execute_tool","tool_args":{"tool_name":"bugs_get","tool_args":{"workspace_id":"1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "user-tapd_mcp_http" || got.ToolName != "proxy_execute_tool" {
		t.Fatalf("server/tool: %+v", got)
	}
	if got.Arguments["tool_name"] != "bugs_get" {
		t.Fatalf("proxy args: %+v", got.Arguments)
	}
	inner, _ := got.Arguments["tool_args"].(map[string]any)
	if inner["workspace_id"] != "1" {
		t.Fatalf("nested tool_args: %+v", got.Arguments)
	}
}

func TestDecodeMCPToolPayloadLiftsNestedWrapperAndUserPrefix(t *testing.T) {
	t.Parallel()

	got, err := DecodeMCPToolPayload([]byte(`{"args":{"server":"tapd_mcp_http","tool_name":"lookup_tapd_tool","tool_args":{"task_description":"查询缺陷"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "user-tapd_mcp_http" {
		t.Fatalf("expected user- prefix, got %+v", got)
	}
	if got.ToolName != "lookup_tapd_tool" {
		t.Fatalf("toolName: %+v", got)
	}
	if got.Arguments["task_description"] != "查询缺陷" {
		t.Fatalf("lifted tool_args: %+v", got.Arguments)
	}
}

func TestInferMCPServerIdentifierUsesLastHyphen(t *testing.T) {
	t.Parallel()

	if got := InferMCPServerIdentifier("user-tapd_mcp_http-lookup_tapd_tool"); got != "user-tapd_mcp_http" {
		t.Fatalf("got %q", got)
	}
	if got := InferMCPServerIdentifier("cursor-ide-browser-browser_navigate"); got != "cursor-ide-browser" {
		t.Fatalf("got %q", got)
	}
	if got := InferMCPToolName("user-tapd_mcp_http", "user-tapd_mcp_http-lookup_tapd_tool"); got != "lookup_tapd_tool" {
		t.Fatalf("tool %q", got)
	}
}

func TestDecodeMCPToolPayloadKeepsOfficialProxyArguments(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"server":   "user-tapd_mcp_http",
		"toolName": "proxy_execute_tool",
		"arguments": map[string]any{
			"tool_name": "bugs_get",
			"tool_args": map[string]any{"workspace_id": "70195660"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMCPToolPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolName != "proxy_execute_tool" || got.Arguments["tool_name"] != "bugs_get" {
		t.Fatalf("must not steal nested proxy fields: %+v", got)
	}
}
