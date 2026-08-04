package forwarder

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func TestProjectLegacyCheckpointKeepsForkContextWithoutDanglingTurnBlobs(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{
		Text:      "parent question",
		MessageId: "message-1",
	})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newAssistantTextEntry(1, "request-1", "parent answer", "", ""),
		},
	}

	state, err := NewHistoryProjector().ProjectLegacyCheckpoint(conversation)
	if err != nil {
		t.Fatalf("ProjectLegacyCheckpoint() error = %v", err)
	}
	if len(state.GetTurns()) != 0 {
		t.Fatalf("ProjectLegacyCheckpoint() turns = %d, want 0 dangling inline blobs", len(state.GetTurns()))
	}

	messages, err := importedConversationStateModelMessages(state)
	if err != nil {
		t.Fatalf("importedConversationStateModelMessages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("imported messages = %d, want parent user and assistant context", len(messages))
	}
	if messages[0].Role != "user" || !strings.Contains(messages[0].Content, "parent question") {
		t.Fatalf("first imported message = %#v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "parent answer" {
		t.Fatalf("second imported message = %#v", messages[1])
	}
}
