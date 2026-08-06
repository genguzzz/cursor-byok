package forwarder

import (
	"crypto/sha256"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func TestProjectCheckpointProjectionBuildsResolvableForkState(t *testing.T) {
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

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	state := projection.State
	if len(state.GetTurns()) != 1 {
		t.Fatalf("ProjectCheckpointProjection() turns = %d, want 1 Blob-backed turn", len(state.GetTurns()))
	}
	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || string(blob.ID) != string(digest[:]) {
			t.Fatalf("invalid content-addressed Blob id=%x", blob.ID)
		}
		blobs[string(blob.ID)] = blob.Data
	}
	turnPayload, ok := blobs[string(state.GetTurns()[0])]
	if !ok {
		t.Fatal("turn references a missing Blob")
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(turnPayload, turn); err != nil {
		t.Fatalf("decode turn Blob: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("turn Blob does not contain an agent turn")
	}
	if _, ok := blobs[string(agentTurn.GetUserMessage())]; !ok {
		t.Fatal("turn references a missing user message Blob")
	}
	for _, stepID := range agentTurn.GetSteps() {
		if _, ok := blobs[string(stepID)]; !ok {
			t.Fatal("turn references a missing step Blob")
		}
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

func TestProjectCheckpointProjectionKeepsForkPointIsolatedFromLaterHistory(t *testing.T) {
	firstUser, err := protojson.Marshal(&agentv1.UserMessage{Text: "first question", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal first user message: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: firstUser},
			newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		},
	}
	projector := NewHistoryProjector()
	midpoint, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("midpoint projection: %v", err)
	}

	secondUser, err := protojson.Marshal(&agentv1.UserMessage{Text: "second question", MessageId: "message-2"})
	if err != nil {
		t.Fatalf("marshal second user message: %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		{TurnSeq: 2, RequestID: "request-2", Role: "user", Kind: "user_message", Payload: secondUser},
		newAssistantTextEntry(2, "request-2", "second answer", "", ""),
	})
	latest, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("latest projection: %v", err)
	}

	midpointMessages, err := importedConversationStateModelMessages(midpoint.State)
	if err != nil {
		t.Fatalf("import midpoint messages: %v", err)
	}
	latestMessages, err := importedConversationStateModelMessages(latest.State)
	if err != nil {
		t.Fatalf("import latest messages: %v", err)
	}
	if len(midpoint.State.GetTurns()) != 1 || len(midpointMessages) != 2 {
		t.Fatalf("midpoint turns=%d messages=%d, want 1 turn and 2 messages", len(midpoint.State.GetTurns()), len(midpointMessages))
	}
	if len(latest.State.GetTurns()) != 2 || len(latestMessages) != 4 {
		t.Fatalf("latest turns=%d messages=%d, want 2 turns and 4 messages", len(latest.State.GetTurns()), len(latestMessages))
	}
	if midpointMessages[1].Content != "first answer" || latestMessages[3].Content != "second answer" {
		t.Fatalf("fork snapshots are not isolated: midpoint=%#v latest=%#v", midpointMessages, latestMessages)
	}
}
