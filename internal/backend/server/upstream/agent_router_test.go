package upstream

import (
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/agent/protocol"
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

func TestClassifyMessageChildExploreOverrideRoutesLocal(t *testing.T) {
	router := NewAgentRouter(nil, nil, t.TempDir())
	reqCtx := &RequestContext{Deps: &Dependencies{SystemSettingService: fakeSettings{adapters: []legacyruntime.ModelAdapterConfig{
		{ID: "752cecad0d652981", ModelID: "deepseek-v4-pro-ioa", DisplayName: "Deepseek Pro"},
	}}}}
	child := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId:   proto.String("explore-child"),
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{{
					SubagentType: "explore",
					Selection: &agentv1.SubagentModelOverride_Model{
						Model: &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
					},
				}},
			},
		},
	}
	if backend := router.classifyMessage(reqCtx, child); backend != AgentBackendLocal {
		t.Fatalf("child with local explore override must be local, got %s", backend)
	}
	if forwarder.ExtractEffectiveRunModelID(child) != "752cecad0d652981" {
		t.Fatalf("effective = %q", forwarder.ExtractEffectiveRunModelID(child))
	}
}

func TestClassifyMessageChildExploreOverrideRoutesUpstream(t *testing.T) {
	router := NewAgentRouter(nil, nil, t.TempDir())
	reqCtx := &RequestContext{Deps: &Dependencies{SystemSettingService: fakeSettings{adapters: []legacyruntime.ModelAdapterConfig{
		{ID: "752cecad0d652981", ModelID: "deepseek-v4-pro-ioa", DisplayName: "Deepseek Pro"},
	}}}}
	child := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId:   proto.String("explore-child-official"),
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{{
					SubagentType: "explore",
					Selection: &agentv1.SubagentModelOverride_Model{
						Model: &agentv1.RequestedModel{ModelId: "grok-4.5"},
					},
				}},
			},
		},
	}
	if backend := router.classifyMessage(reqCtx, child); backend != AgentBackendUpstream {
		t.Fatalf("child with official explore override must be upstream, got %s", backend)
	}
}

func TestClassifyBidiChildExploreOverrideRewritesAndRoutesLocal(t *testing.T) {
	router := NewAgentRouter(nil, nil, t.TempDir())
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId:   proto.String("explore-child"),
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{{
					SubagentType: "explore",
					Selection: &agentv1.SubagentModelOverride_Model{
						Model: &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
					},
				}},
			},
		},
	}
	rawMsg, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	appendReq := &aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "req-explore-child"},
		Data:      hex.EncodeToString(rawMsg),
	}
	body, err := proto.Marshal(appendReq)
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := &RequestContext{
		ContentType: "application/proto",
		RequestBody: body,
		Headers:     make(http.Header),
		Request:     &http.Request{Header: make(http.Header)},
		Deps: &Dependencies{SystemSettingService: fakeSettings{adapters: []legacyruntime.ModelAdapterConfig{
			{ID: "752cecad0d652981", ModelID: "deepseek-v4-pro-ioa", DisplayName: "Deepseek Pro"},
		}}},
	}
	backend, err := router.classifyBidi(reqCtx)
	if err != nil {
		t.Fatal(err)
	}
	if backend != AgentBackendLocal {
		t.Fatalf("backend = %s", backend)
	}
	decoded := &aiserverv1.BidiAppendRequest{}
	if err := decodeProtoPayload(reqCtx.ContentType, reqCtx.RequestBody, decoded); err != nil {
		t.Fatal(err)
	}
	clientMessage, _, err := protocol.DecodeAgentClientMessage(decoded.GetData())
	if err != nil {
		t.Fatal(err)
	}
	if got := clientMessage.GetRunRequest().GetRequestedModel().GetModelId(); got != "752cecad0d652981" {
		t.Fatalf("rewritten model = %q", got)
	}

	// sticky 后再次进入仍应改写 body，避免 prewarm 绑定后 run_request 带着父模型回源。
	reqCtx.RequestBody = body
	backend, err = router.classifyBidi(reqCtx)
	if err != nil {
		t.Fatal(err)
	}
	if backend != AgentBackendLocal {
		t.Fatalf("sticky backend = %s", backend)
	}
	decoded = &aiserverv1.BidiAppendRequest{}
	if err := decodeProtoPayload(reqCtx.ContentType, reqCtx.RequestBody, decoded); err != nil {
		t.Fatal(err)
	}
	clientMessage, _, err = protocol.DecodeAgentClientMessage(decoded.GetData())
	if err != nil {
		t.Fatal(err)
	}
	if got := clientMessage.GetRunRequest().GetRequestedModel().GetModelId(); got != "752cecad0d652981" {
		t.Fatalf("sticky rewritten model = %q", got)
	}
}

func TestRewriteBidiAppendAgentMessageUpdatesRequestedModel(t *testing.T) {
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{{
					SubagentType: "explore",
					Selection: &agentv1.SubagentModelOverride_Model{
						Model: &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
					},
				}},
			},
		},
	}
	if !forwarder.ApplyEffectiveChildRunModel(message) {
		t.Fatal("expected apply")
	}
	rawMsg, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	appendReq := &aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "req-child"},
		Data:      hex.EncodeToString(rawMsg),
	}
	// Build an original body with parent model, then rewrite after apply.
	originalMessage := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				SubagentTypeName: proto.String("explore"),
				RequestedModel:   &agentv1.RequestedModel{ModelId: "grok-4.5"},
				SubagentModelOverrides: []*agentv1.SubagentModelOverride{{
					SubagentType: "explore",
					Selection: &agentv1.SubagentModelOverride_Model{
						Model: &agentv1.RequestedModel{ModelId: "752cecad0d652981"},
					},
				}},
			},
		},
	}
	originalRaw, err := proto.Marshal(originalMessage)
	if err != nil {
		t.Fatal(err)
	}
	originalAppend := &aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: "req-child"},
		Data:      hex.EncodeToString(originalRaw),
	}
	originalBody, err := proto.Marshal(originalAppend)
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := &RequestContext{
		ContentType: "application/proto",
		RequestBody: originalBody,
		Headers:     make(http.Header),
		Request:     &http.Request{Header: make(http.Header)},
	}
	if err := rewriteBidiAppendAgentMessage(reqCtx, originalBody, appendReq, message); err != nil {
		t.Fatal(err)
	}
	decoded := &aiserverv1.BidiAppendRequest{}
	if err := decodeProtoPayload(reqCtx.ContentType, reqCtx.RequestBody, decoded); err != nil {
		t.Fatal(err)
	}
	clientMessage, _, err := protocol.DecodeAgentClientMessage(decoded.GetData())
	if err != nil {
		t.Fatal(err)
	}
	if got := clientMessage.GetRunRequest().GetRequestedModel().GetModelId(); got != "752cecad0d652981" {
		t.Fatalf("rewritten model = %q", got)
	}
}
