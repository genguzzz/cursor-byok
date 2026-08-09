package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func TestEstimateCheckpointPromptTokenBreakdownOfficialCategories(t *testing.T) {
	taskTool := json.RawMessage(`{"type":"function","function":{"name":"Task","description":"Launch agents.\n\nAvailable subagent_types and a quick description of what they do:\n- explore: Fast explorer\n\nAvailable models:\n- fast"}}`)
	readTool := json.RawMessage(`{"type":"function","function":{"name":"Read","description":"Read a file"}}`)
	mcpTool := json.RawMessage(`{"type":"function","function":{"name":"CallMcpTool","description":"Call MCP"}}`)

	compiled := CompiledConversation{
		Messages: []modeladapter.Message{
			{Role: "system", Content: "You are a coding agent."},
			{Role: "user", Content: strings.Join([]string{
				`<rules><user_rules>Always respond in 中文</user_rules></rules>`,
				`<agent_skills><available_skills><agent_skill fullPath="/tmp/x">demo</agent_skill></available_skills></agent_skills>`,
				`<mcp_file_system>Available MCP servers</mcp_file_system>`,
				"Please investigate compaction.",
			}, "\n\n")},
			{Role: "assistant", Content: "Looking into it."},
		},
		Tools: []json.RawMessage{taskTool, readTool, mcpTool},
	}

	snapshot := estimateCheckpointPromptTokenBreakdown(compiled, true, 0, 256000)
	if snapshot == nil {
		t.Fatal("expected breakdown snapshot")
	}
	if snapshot.GetMaxTokens() != 256000 {
		t.Fatalf("max tokens=%d", snapshot.GetMaxTokens())
	}
	if len(snapshot.GetCategories()) != 8 {
		t.Fatalf("expected 8 categories, got %d", len(snapshot.GetCategories()))
	}

	byID := map[string]*struct {
		label  string
		tokens uint32
		chars  uint32
	}{}
	for _, category := range snapshot.GetCategories() {
		chars := uint32(0)
		if category.CharacterCount != nil {
			chars = *category.CharacterCount
		}
		byID[category.GetId()] = &struct {
			label  string
			tokens uint32
			chars  uint32
		}{label: category.GetLabel(), tokens: category.GetEstimatedTokens(), chars: chars}
	}

	expect := map[string]string{
		"system_prompt":           "System prompt",
		"tools":                   "Tool definitions",
		"rules":                   "Rules",
		"skills":                  "Skills",
		"mcp":                     "MCP & dynamic tools",
		"subagents":               "Subagent definitions",
		"summarized_conversation": "Summarized conversation",
		"conversation":            "Conversation",
	}
	for id, label := range expect {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("missing category %s", id)
		}
		if got.label != label {
			t.Fatalf("category %s label=%q want %q", id, got.label, label)
		}
	}
	if byID["system_prompt"].tokens == 0 || byID["system_prompt"].chars == 0 {
		t.Fatalf("system_prompt should be counted: %+v", byID["system_prompt"])
	}
	if byID["rules"].tokens == 0 || byID["skills"].tokens == 0 {
		t.Fatalf("rules/skills should be counted: rules=%+v skills=%+v", byID["rules"], byID["skills"])
	}
	if byID["mcp"].tokens == 0 {
		t.Fatalf("mcp should include file system + CallMcpTool: %+v", byID["mcp"])
	}
	if byID["subagents"].tokens == 0 {
		t.Fatalf("subagents should peel Task tool section: %+v", byID["subagents"])
	}
	if byID["tools"].tokens == 0 {
		t.Fatalf("tools should include Read + Task remainder: %+v", byID["tools"])
	}
	if byID["conversation"].tokens == 0 {
		t.Fatalf("conversation should keep non-section user/assistant text: %+v", byID["conversation"])
	}
	if byID["summarized_conversation"].chars != 0 {
		t.Fatalf("summarized_conversation should be empty: %+v", byID["summarized_conversation"])
	}
}

func TestBuildPromptContextUsageTreeOfficialShape(t *testing.T) {
	taskTool := json.RawMessage(`{"type":"function","function":{"name":"Task","description":"Launch agents.\n\nAvailable subagent_types and a quick description of what they do:\n- explore: Fast explorer"}}`)
	readTool := json.RawMessage(`{"type":"function","function":{"name":"Read","description":"Read a file"}}`)
	mcpTool := json.RawMessage(`{"type":"function","function":{"name":"CallMcpTool","description":"Call MCP"}}`)
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{
			{Role: "system", Content: "You are a coding agent."},
			{Role: "user", Content: strings.Join([]string{
				`<rules><user_rules>Always respond in 中文</user_rules></rules>`,
				`<agent_skills><available_skills><agent_skill fullPath="/tmp/x">demo</agent_skill></available_skills></agent_skills>`,
				"Please investigate.",
			}, "\n\n")},
		},
		Tools: []json.RawMessage{taskTool, readTool, mcpTool},
	}
	breakdown := estimateCheckpointPromptTokenBreakdown(compiled, true, 0, 256000)
	tree := buildPromptContextUsageTree(compiled, true, breakdown)
	if tree == nil {
		t.Fatal("expected usage tree")
	}
	if tree.GetSchemaVersion() != 1 {
		t.Fatalf("schema_version=%d", tree.GetSchemaVersion())
	}
	byID := map[string]*struct {
		kind       string
		categoryID string
		parentID   string
	}{}
	for _, node := range tree.GetNodes() {
		parent := ""
		if node.ParentId != nil {
			parent = *node.ParentId
		}
		byID[node.GetId()] = &struct {
			kind       string
			categoryID string
			parentID   string
		}{kind: node.GetKind(), categoryID: node.GetCategoryId(), parentID: parent}
	}
	for _, id := range []string{
		"system_prompt", "tools", "rules", "skills", "mcp", "subagents", "summarized_conversation", "conversation",
	} {
		node, ok := byID["category:"+id]
		if !ok {
			t.Fatalf("missing category node %s", id)
		}
		if node.kind != "category" || node.categoryID != id {
			t.Fatalf("category node %+v", node)
		}
	}
	if _, ok := byID["tool:1"]; !ok {
		t.Fatal("expected Read tool_definition child")
	}
	if byID["tool:1"].kind != "tool_definition" || byID["tool:1"].parentID != "category:tools" {
		t.Fatalf("read tool node %+v", byID["tool:1"])
	}
	if _, ok := byID["tool:2"]; !ok || byID["tool:2"].parentID != "category:mcp" {
		t.Fatalf("mcp tool node %+v", byID["tool:2"])
	}
	if _, ok := byID["skill:0"]; !ok || byID["skill:0"].kind != "skill" {
		t.Fatalf("skill child %+v", byID["skill:0"])
	}
	if _, ok := byID["rule:0"]; !ok || byID["rule:0"].kind != "rule" {
		t.Fatalf("rule child %+v", byID["rule:0"])
	}
}

func TestResolveCompactionReserveTokensForWindowOfficialRatio(t *testing.T) {
	cases := []struct {
		window int64
		want   int64
	}{
		{500_000, 50_000},
		{256_000, 25_600},
		{100_000, 10_000},
		{80_000, 10_000},
		{0, 10_000},
	}
	for _, tc := range cases {
		got := resolveCompactionReserveTokensForWindow(tc.window)
		if got != tc.want {
			t.Fatalf("window=%d reserve=%d want=%d", tc.window, got, tc.want)
		}
	}
}
