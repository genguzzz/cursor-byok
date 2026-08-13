package forwarder

import (
	"path/filepath"
	"testing"

	"cursor/gen/agentv1"
)

func newTestBroker(t *testing.T) *StreamBroker {
	t.Helper()
	return newStreamBrokerWithPath(filepath.Join(t.TempDir(), "backlog.db"))
}

func TestBrokerBacklogPersistsToStore(t *testing.T) {
	broker := newTestBroker(t)
	if _, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model-name", agentv1.AgentMode_AGENT_MODE_AGENT, "hello"); err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	total := 100
	for i := 0; i < total; i++ {
		if err := broker.Publish("request-1", StreamEvent{Message: &agentv1.AgentServerMessage{}}); err != nil {
			t.Fatalf("Publish(%d) error = %v", i, err)
		}
	}
	if count := broker.store.count("request-1"); count != total {
		t.Fatalf("store count = %d, want %d", count, total)
	}
}

func TestBrokerReadFromCursorBySequence(t *testing.T) {
	broker := newTestBroker(t)
	if _, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model-name", agentv1.AgentMode_AGENT_MODE_AGENT, "hello"); err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := broker.Publish("request-1", StreamEvent{Message: &agentv1.AgentServerMessage{}}); err != nil {
			t.Fatalf("Publish(%d) error = %v", i, err)
		}
	}
	events, err := broker.ReadFromCursor("request-1", 0)
	if err != nil {
		t.Fatalf("ReadFromCursor(0) error = %v", err)
	}
	if len(events) != 10 {
		t.Fatalf("ReadFromCursor(0) = %d events, want 10", len(events))
	}
	if events[0].Seq != 1 {
		t.Fatalf("first seq = %d, want 1", events[0].Seq)
	}
	events, err = broker.ReadFromCursor("request-1", 5)
	if err != nil {
		t.Fatalf("ReadFromCursor(5) error = %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("ReadFromCursor(5) = %d events, want 5", len(events))
	}
	if events[0].Seq != 6 {
		t.Fatalf("first seq after 5 = %d, want 6", events[0].Seq)
	}
	events, err = broker.ReadFromCursor("request-1", 10)
	if err != nil {
		t.Fatalf("ReadFromCursor(10) error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("ReadFromCursor(10) = %d events, want 0", len(events))
	}
}

func TestBrokerReadFromCursorBatchesAtReadBatchSize(t *testing.T) {
	broker := newTestBroker(t)
	if _, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model-name", agentv1.AgentMode_AGENT_MODE_AGENT, "hello"); err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	total := backlogReadBatchSize + 10
	for i := 0; i < total; i++ {
		if err := broker.Publish("request-1", StreamEvent{Message: &agentv1.AgentServerMessage{}}); err != nil {
			t.Fatalf("Publish(%d) error = %v", i, err)
		}
	}
	events, err := broker.ReadFromCursor("request-1", 0)
	if err != nil {
		t.Fatalf("ReadFromCursor(0) error = %v", err)
	}
	if len(events) != backlogReadBatchSize {
		t.Fatalf("first batch = %d events, want %d", len(events), backlogReadBatchSize)
	}
	// 继续用最后一条的 Seq 读剩余事件
	lastSeq := events[len(events)-1].Seq
	events, err = broker.ReadFromCursor("request-1", lastSeq)
	if err != nil {
		t.Fatalf("ReadFromCursor(last) error = %v", err)
	}
	if len(events) != total-int(lastSeq) {
		t.Fatalf("second batch = %d events, want %d", len(events), total-int(lastSeq))
	}
}

func TestBrokerRemoveCleansStore(t *testing.T) {
	broker := newTestBroker(t)
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model-name", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := broker.Publish("request-1", StreamEvent{Message: &agentv1.AgentServerMessage{}}); err != nil {
			t.Fatalf("Publish(%d) error = %v", i, err)
		}
	}
	stream.mu.Lock()
	stream.Status = StreamStatusCompleted
	stream.mu.Unlock()
	if !broker.RemoveIfIdle("request-1") {
		t.Fatalf("RemoveIfIdle() = false, want true")
	}
	if count := broker.store.count("request-1"); count != 0 {
		t.Fatalf("store count after remove = %d, want 0", count)
	}
}