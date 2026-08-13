package forwarder

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func TestBuildCompactedContextEntriesCarriesSkillAndMCPManifest(t *testing.T) {
	t.Parallel()

	requestContext := &agentv1.RequestContext{
		SkillOptions: &agentv1.SkillOptions{
			SkillDescriptors: []*agentv1.SkillDescriptor{
				{
					Name:           "demo",
					Description:    "Demo skill.",
					ReadmeFilePath: "/repo/.agents/skills/demo/SKILL.md",
					FolderPath:     "/repo/.agents/skills/demo",
					Enabled:        true,
				},
			},
		},
		McpFileSystemOptions: &agentv1.McpFileSystemOptions{
			Enabled:             true,
			WorkspaceProjectDir: "/repo",
			McpDescriptors: []*agentv1.McpDescriptor{
				{
					ServerIdentifier: "demo-server",
					ServerName:       "demo-server",
				},
			},
		},
	}
	payload, err := protojson.Marshal(requestContext)
	if err != nil {
		t.Fatal(err)
	}

	conversation := &ConversationFile{
		Entries: []HistoryEntry{
			{TurnSeq: 1, RequestID: "req-1", Role: "user", Kind: "request_context", Payload: payload},
		},
	}
	plan := &PendingCompaction{
		Trigger:          "auto",
		CurrentTurnSeq:   4,
		CurrentRequestID: "req-4",
	}

	entries, err := buildCompactedContextEntries(conversation, plan, "summary text")
	if err != nil {
		t.Fatal(err)
	}

	var manifest *HistoryEntry
	for index := range entries {
		if strings.TrimSpace(entries[index].Kind) == "request_context" {
			manifest = &entries[index]
			break
		}
	}
	if manifest == nil {
		t.Fatal("expected compacted entries to include a request_context manifest")
	}
	if strings.TrimSpace(entries[0].Kind) != "compacted_summary" {
		t.Fatalf("expected compacted_summary first, got %q", entries[0].Kind)
	}

	restored := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(manifest.Payload, restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.GetSkillOptions().GetSkillDescriptors()) != 1 {
		t.Fatalf("expected 1 skill descriptor, got %d", len(restored.GetSkillOptions().GetSkillDescriptors()))
	}
	if len(restored.GetMcpFileSystemOptions().GetMcpDescriptors()) != 1 {
		t.Fatalf("expected 1 mcp descriptor, got %d", len(restored.GetMcpFileSystemOptions().GetMcpDescriptors()))
	}

	projector := NewHistoryProjector()
	projected := &ConversationFile{Entries: append([]HistoryEntry(nil), entries...)}
	messages, err := projector.ProjectPromptReplay(projected)
	if err != nil {
		t.Fatal(err)
	}
	combined := ""
	for _, message := range messages {
		combined += message.Content + "\n"
	}
	if !strings.Contains(combined, "<agent_skills>") {
		t.Fatal("projected replay lost agent_skills section after compaction")
	}
	if !strings.Contains(combined, "<mcp_file_system>") {
		t.Fatal("projected replay lost mcp_file_system section after compaction")
	}
}

func TestBuildCompactedContextEntriesPrefersFreshManifest(t *testing.T) {
	t.Parallel()

	staleContext := &agentv1.RequestContext{
		SkillOptions: &agentv1.SkillOptions{
			SkillDescriptors: []*agentv1.SkillDescriptor{
				{
					Name:           "old",
					Description:    "Old skill.",
					ReadmeFilePath: "/repo/.agents/skills/old/SKILL.md",
					FolderPath:     "/repo/.agents/skills/old",
					Enabled:        true,
				},
			},
		},
	}
	stalePayload, err := protojson.Marshal(staleContext)
	if err != nil {
		t.Fatal(err)
	}

	conversation := &ConversationFile{
		Entries: []HistoryEntry{
			{TurnSeq: 1, RequestID: "req-1", Role: "user", Kind: "request_context", Payload: stalePayload},
		},
	}

	freshManifest := &agentv1.RequestContext{
		SkillOptions: &agentv1.SkillOptions{
			SkillDescriptors: []*agentv1.SkillDescriptor{
				{
					Name:           "new",
					Description:    "New skill after restart.",
					ReadmeFilePath: "/repo/.agents/skills/new/SKILL.md",
					FolderPath:     "/repo/.agents/skills/new",
					Enabled:        true,
				},
			},
		},
	}
	plan := &PendingCompaction{
		Trigger:          "auto",
		CurrentTurnSeq:   5,
		CurrentRequestID: "req-5",
		StaticManifest:   freshManifest,
	}

	entries, err := buildCompactedContextEntries(conversation, plan, "summary")
	if err != nil {
		t.Fatal(err)
	}
	var manifest *HistoryEntry
	for index := range entries {
		if strings.TrimSpace(entries[index].Kind) == "request_context" {
			manifest = &entries[index]
			break
		}
	}
	if manifest == nil {
		t.Fatal("expected a request_context manifest entry")
	}
	restored := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(manifest.Payload, restored); err != nil {
		t.Fatal(err)
	}
	descriptors := restored.GetSkillOptions().GetSkillDescriptors()
	if len(descriptors) != 1 || descriptors[0].GetName() != "new" {
		t.Fatalf("expected fresh manifest skill 'new', got %+v", descriptors)
	}
}

func TestBuildCompactedContextEntriesWithoutManifest(t *testing.T) {
	t.Parallel()

	conversation := &ConversationFile{
		Entries: []HistoryEntry{
			{TurnSeq: 1, RequestID: "req-1", Role: "user", Kind: "user_message", Payload: []byte(`{"text":"hi"}`)},
		},
	}
	plan := &PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "req-2"}

	entries, err := buildCompactedContextEntries(conversation, plan, "summary")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) == "request_context" {
			t.Fatal("did not expect a request_context manifest when history has none")
		}
	}
}

func TestBuildCompactedContextEntriesFillsMCPFromHistoryWhenLiveIsSkillsOnly(t *testing.T) {
	t.Parallel()

	// Turn-1 request_context carries both skills and the MCP file-system surface.
	turnOne := &agentv1.RequestContext{
		SkillOptions: &agentv1.SkillOptions{
			SkillDescriptors: []*agentv1.SkillDescriptor{
				{
					Name:           "demo",
					Description:    "Demo skill.",
					ReadmeFilePath: "/repo/.agents/skills/demo/SKILL.md",
					FolderPath:     "/repo/.agents/skills/demo",
					Enabled:        true,
				},
			},
		},
		McpFileSystemOptions: &agentv1.McpFileSystemOptions{
			Enabled:             true,
			WorkspaceProjectDir: "/repo",
			McpDescriptors: []*agentv1.McpDescriptor{
				{ServerIdentifier: "ida-pro-mcp", ServerName: "ida-pro-mcp"},
			},
		},
	}
	turnOnePayload, err := protojson.Marshal(turnOne)
	if err != nil {
		t.Fatal(err)
	}

	conversation := &ConversationFile{
		Entries: []HistoryEntry{
			{TurnSeq: 1, RequestID: "req-1", Role: "user", Kind: "request_context", Payload: turnOnePayload},
		},
	}

	// A later turn only re-sent skills, dropping the MCP file-system surface.
	skillsOnly := &agentv1.RequestContext{
		SkillOptions: &agentv1.SkillOptions{
			SkillDescriptors: []*agentv1.SkillDescriptor{
				{
					Name:           "new",
					Description:    "New skill after restart.",
					ReadmeFilePath: "/repo/.agents/skills/new/SKILL.md",
					FolderPath:     "/repo/.agents/skills/new",
					Enabled:        true,
				},
			},
		},
	}
	plan := &PendingCompaction{
		Trigger:          "auto",
		CurrentTurnSeq:   5,
		CurrentRequestID: "req-5",
		StaticManifest:   skillsOnly,
	}

	entries, err := buildCompactedContextEntries(conversation, plan, "summary")
	if err != nil {
		t.Fatal(err)
	}
	var manifest *HistoryEntry
	for index := range entries {
		if strings.TrimSpace(entries[index].Kind) == "request_context" {
			manifest = &entries[index]
			break
		}
	}
	if manifest == nil {
		t.Fatal("expected a request_context manifest entry")
	}
	restored := &agentv1.RequestContext{}
	if err := protojson.Unmarshal(manifest.Payload, restored); err != nil {
		t.Fatal(err)
	}
	if got := len(restored.GetMcpFileSystemOptions().GetMcpDescriptors()); got != 1 {
		t.Fatalf("expected MCP file-system descriptor to survive skills-only live manifest, got %d", got)
	}
	if descriptors := restored.GetSkillOptions().GetSkillDescriptors(); len(descriptors) != 1 || descriptors[0].GetName() != "new" {
		t.Fatalf("expected live skill 'new' to win, got %+v", descriptors)
	}
}

func TestExtractStaticManifestPreservesMcpMetaToolOptions(t *testing.T) {
	t.Parallel()

	source := &agentv1.RequestContext{
		McpMetaToolOptions: &agentv1.McpMetaToolOptions{
			Enabled: true,
			McpDescriptors: []*agentv1.McpDescriptor{
				{ServerIdentifier: "tapd", ServerName: "tapd"},
			},
		},
	}
	manifest := extractStaticManifest(source)
	if manifest == nil {
		t.Fatal("expected a non-nil manifest")
	}
	if got := len(manifest.GetMcpMetaToolOptions().GetMcpDescriptors()); got != 1 {
		t.Fatalf("expected 1 MCP meta-tool descriptor, got %d", got)
	}
}