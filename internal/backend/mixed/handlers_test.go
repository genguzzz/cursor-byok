package mixed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"cursor/internal/backend/server"
	"cursor/internal/backend/server/upstream"
)

func TestIsLocalAiServicePath(t *testing.T) {
	if !IsLocalAiServicePath("/aiserver.v1.AiService/NameTab", true) {
		t.Fatal("NameTab should stay local when tabRenamer enabled")
	}
	if IsLocalAiServicePath("/aiserver.v1.AiService/NameTab", false) {
		t.Fatal("NameTab should not be local when tabRenamer disabled")
	}
	if !IsLocalAiServicePath("/aiserver.v1.AiService/NameAgent", true) {
		t.Fatal("NameAgent should stay local when tabRenamer enabled")
	}
	if IsLocalAiServicePath("/aiserver.v1.AiService/NameAgent", false) {
		t.Fatal("NameAgent should not be local when tabRenamer disabled")
	}
	if !IsLocalAiServicePath("/aiserver.v1.AiService/CountTokens", false) {
		t.Fatal("CountTokens should stay local regardless of tabRenamer")
	}
	if IsLocalAiServicePath("/aiserver.v1.AiService/AvailableModels", false) {
		t.Fatal("AvailableModels is mixed-catalog, not catch-all local")
	}
}

func TestAIServiceCatchAllNameTabForwardsWhenTabRenamerDisabled(t *testing.T) {
	var gotPath, gotAuth string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-name"))
	}))
	t.Cleanup(upstreamServer.Close)

	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("local-name"))
	})

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL = mustParseURL(upstreamServer.URL + req.URL.Path)
		cloned.RequestURI = ""
		cloned.Host = cloned.URL.Host
		return upstreamServer.Client().Transport.RoundTrip(cloned)
	})}

	handler := AIServiceCatchAll(true, local, upstream.Dependencies{HTTPClient: client}, func() bool { return false })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/NameTab", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	target := mustParseURL("https://api2.cursor.sh/aiserver.v1.AiService/NameTab")
	if err := handler(&server.Context{Writer: rec, Request: req, UpstreamURL: target}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if localCalled {
		t.Fatal("local handler must not run when tabRenamer disabled")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "upstream-name" {
		t.Fatalf("body=%q, want upstream-name", body)
	}
	if gotPath != "/aiserver.v1.AiService/NameTab" {
		t.Fatalf("upstream path=%q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization=%q", gotAuth)
	}
}

func TestAIServiceCatchAllNameAgentStaysLocalWhenTabRenamerEnabled(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "local-name")
	})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be called when tabRenamer enabled")
	}))
	t.Cleanup(upstreamServer.Close)

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL = mustParseURL(upstreamServer.URL + req.URL.Path)
		cloned.RequestURI = ""
		cloned.Host = cloned.URL.Host
		return upstreamServer.Client().Transport.RoundTrip(cloned)
	})}

	handler := AIServiceCatchAll(true, local, upstream.Dependencies{HTTPClient: client}, func() bool { return true })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/NameAgent", nil)
	if err := handler(&server.Context{Writer: rec, Request: req}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !localCalled {
		t.Fatal("local handler should run when tabRenamer enabled")
	}
	if rec.Body.String() != "local-name" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestAIServiceCatchAllMixedOffAlwaysLocal(t *testing.T) {
	localCalled := false
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "local")
	})
	handler := AIServiceCatchAll(false, local, upstream.Dependencies{}, func() bool { return false })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.AiService/NameTab", nil)
	if err := handler(&server.Context{Writer: rec, Request: req}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !localCalled {
		t.Fatal("mixed off must keep NameTab on local handler")
	}
}

func TestForwardOrNotFoundCmdKMixedOffReturns404(t *testing.T) {
	handler := ForwardOrNotFound(false, upstream.Dependencies{}, "cmdk_service")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.CmdKService/StreamCmdK", nil)
	if err := handler(&server.Context{Writer: rec, Request: req}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 when mixed off", rec.Code)
	}
}

func TestForwardOrNotFoundCmdKMixedOnForwards(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/aiserver.v1.CmdKService/StreamCmdK" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstreamServer.Close)

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL = mustParseURL(upstreamServer.URL + req.URL.Path)
		cloned.RequestURI = ""
		cloned.Host = cloned.URL.Host
		return upstreamServer.Client().Transport.RoundTrip(cloned)
	})}

	target := mustParseURL("https://api2.cursor.sh/aiserver.v1.CmdKService/StreamCmdK")
	handler := ForwardOrNotFound(true, upstream.Dependencies{HTTPClient: client}, "cmdk_service")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/aiserver.v1.CmdKService/StreamCmdK", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	if err := handler(&server.Context{Writer: rec, Request: req, UpstreamURL: target}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200 forward", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func mustParseURL(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}
