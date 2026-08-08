package upstream

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/internal/backend/forwarder"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

type fakeSettings struct {
	adapters []legacyruntime.ModelAdapterConfig
}

func (f fakeSettings) ResolveModelAdapters(context.Context) ([]legacyruntime.ModelAdapterConfig, error) {
	return f.adapters, nil
}

func TestIsConfiguredChannelID(t *testing.T) {
	channels := map[string]struct{}{"abc123def4567890": {}}
	if !isConfiguredChannelID("abc123def4567890", channels) {
		t.Fatal("channel id should be local")
	}
	if !isConfiguredChannelID("abc123def4567890:high", channels) {
		t.Fatal("variant prefix should be local")
	}
	if isConfiguredChannelID("claude-4-sonnet", channels) {
		t.Fatal("official model should not be local")
	}
}

func TestAgentRouteTableStickyAndWait(t *testing.T) {
	table := newAgentRouteTable()
	done := make(chan AgentBackend, 1)
	go func() {
		backend, ok := table.Wait("req-1", time.Second)
		if !ok {
			t.Errorf("expected classified backend")
		}
		done <- backend
	}()
	time.Sleep(20 * time.Millisecond)
	table.Bind("req-1", AgentBackendLocal)
	select {
	case backend := <-done:
		if backend != AgentBackendLocal {
			t.Fatalf("backend = %s", backend)
		}
	case <-time.After(time.Second):
		t.Fatal("wait timed out")
	}
	got, ok := table.Lookup("req-1")
	if !ok || got != AgentBackendLocal {
		t.Fatalf("lookup = %s %v", got, ok)
	}
}

func TestAgentRouteTableWaitTimeoutDefaultsUpstream(t *testing.T) {
	table := newAgentRouteTable()
	backend, ok := table.Wait("missing", 30*time.Millisecond)
	if ok {
		t.Fatal("expected unclassified")
	}
	if backend != AgentBackendUpstream {
		t.Fatalf("default backend = %s", backend)
	}
}

func TestClassifyMessageLocalHistoryFallback(t *testing.T) {
	root := t.TempDir()
	conversationID := "conv-local"
	if err := os.MkdirAll(filepath.Join(root, conversationID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, conversationID, "state.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	router := NewAgentRouter(nil, nil, root)
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{ConversationId: proto.String(conversationID)},
		},
	}
	if forwarder.ExtractConversationID(message) != conversationID {
		t.Fatal("conversation id extract failed")
	}
	if !forwarder.LocalHistoryExists(root, conversationID) {
		t.Fatal("expected local history")
	}
	if backend := router.classifyMessage(&RequestContext{}, message); backend != AgentBackendLocal {
		t.Fatalf("backend = %s", backend)
	}
	official := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId: proto.String("other"),
				RequestedModel: &agentv1.RequestedModel{ModelId: "claude-4-sonnet"},
			},
		},
	}
	if backend := router.classifyMessage(&RequestContext{}, official); backend != AgentBackendUpstream {
		t.Fatalf("official backend = %s", backend)
	}

	localChannel := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId: proto.String("new-conv"),
				RequestedModel: &agentv1.RequestedModel{ModelId: "abc123def4567890:high"},
			},
		},
	}
	reqCtx := &RequestContext{Deps: &Dependencies{SystemSettingService: fakeSettings{adapters: []legacyruntime.ModelAdapterConfig{{ID: "abc123def4567890", ModelID: "composer-2.5", DisplayName: "Local Composer"}}}}}
	if backend := router.classifyMessage(reqCtx, localChannel); backend != AgentBackendLocal {
		t.Fatalf("injected channel backend = %s", backend)
	}
	// provider modelID 与官方同名时不得劫持官方路由
	byProviderID := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{RequestedModel: &agentv1.RequestedModel{ModelId: "composer-2.5"}},
		},
	}
	if backend := router.classifyMessage(reqCtx, byProviderID); backend != AgentBackendUpstream {
		t.Fatalf("provider model id colliding with official must be upstream, got %s", backend)
	}
}
