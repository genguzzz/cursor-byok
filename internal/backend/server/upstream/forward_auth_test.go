package upstream

import (
	"net/http"
	"net/url"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

func TestBuildUpstreamRequestPreservesClientAuth(t *testing.T) {
	target, err := url.Parse("https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels")
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := &RequestContext{
		Method:    http.MethodPost,
		TargetURL: target,
		Headers: http.Header{
			"Authorization": []string{"Bearer real-user-token"},
		},
		RequestBody: []byte("body"),
	}
	req, _, err := buildUpstreamRequest(reqCtx, []byte("body"), ForwardOptions{PreserveClientAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer real-user-token" {
		t.Fatalf("preserved auth = %q", got)
	}

	rewritten, _, err := buildUpstreamRequest(reqCtx, []byte("body"), ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "Bearer " + legacyruntime.LocalRelayToken
	if got := rewritten.Header.Get("Authorization"); got != want {
		t.Fatalf("legacy rewrite auth = %q want %q", got, want)
	}
}

func TestBuildUpstreamRequestRewritesLocalTargetWhenPreservingClientAuth(t *testing.T) {
	local, err := url.Parse("/aiserver.v1.AiService/AvailableModels")
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := &RequestContext{
		Method:      http.MethodPost,
		TargetURL:   local,
		Headers:     http.Header{"Authorization": []string{"Bearer real-user-token"}},
		RequestBody: []byte("body"),
	}
	req, _, err := buildUpstreamRequest(reqCtx, []byte("body"), ForwardOptions{PreserveClientAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://api2.cursor.sh/aiserver.v1.AiService/AvailableModels" {
		t.Fatalf("rewritten url = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer real-user-token" {
		t.Fatalf("preserved auth = %q", got)
	}
}

func TestBuildUpstreamRequestKeepsMITMApi5Host(t *testing.T) {
	target, err := url.Parse("https://agentn.global.api5.cursor.sh/agent.v1.AgentService/RunSSE")
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := &RequestContext{
		Method:    http.MethodPost,
		TargetURL: target,
		Headers:   http.Header{"Authorization": []string{"Bearer real-user-token"}},
	}
	req, _, err := buildUpstreamRequest(reqCtx, nil, ForwardOptions{PreserveClientAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != target.String() {
		t.Fatalf("api5 host rewritten unexpectedly: %q", got)
	}
}
