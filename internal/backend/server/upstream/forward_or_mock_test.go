package upstream

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cursor/internal/backend/server"
)

func TestClientAuthForwardOrMockFailClosedWithAuthorization(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(upstreamServer.Close)

	target, err := url.Parse(upstreamServer.URL + "/aiserver.v1.DashboardService/GetPlanInfo")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.DashboardService/GetPlanInfo", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer real-user-token")
	req.Header.Set("Content-Type", "application/json")

	handler := ClientAuthForwardOrMockAction(Dependencies{HTTPClient: upstreamServer.Client()}, CompatRouteConfig{
		Name:          "plan_info",
		StatusCode:    http.StatusOK,
		MockProtoType: "aiserver.v1.GetPlanInfoResponse",
		MockBuilder:   DashboardPlanInfoMockBuilder,
	})
	ctx := &server.Context{
		Writer:      recorder,
		Request:     req,
		StartedAt:   time.Now(),
		UpstreamURL: target,
	}
	if err := handler(ctx); err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 fail-closed, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Ultra") {
		t.Fatal("must not fall back to Ultra mock when client auth present")
	}
}

func TestClientAuthForwardOrMockFallsBackWithoutAuthorization(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(upstreamServer.Close)

	target, err := url.Parse(upstreamServer.URL + "/aiserver.v1.AiService/GetDefaultModelForCli")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/GetDefaultModelForCli", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/proto")

	handler := ClientAuthForwardOrMockAction(Dependencies{HTTPClient: upstreamServer.Client()}, CompatRouteConfig{
		Name:          "default_model_for_cli",
		StatusCode:    http.StatusOK,
		MockProtoType: "aiserver.v1.GetDefaultModelForCliResponse",
		MockBuilder:   DefaultModelForCliMockBuilder,
	})
	ctx := &server.Context{
		Writer:      recorder,
		Request:     req,
		StartedAt:   time.Now(),
		UpstreamURL: target,
	}
	if err := handler(ctx); err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected mock fallback 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}
