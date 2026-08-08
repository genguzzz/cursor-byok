package agentcli

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildAgentArgsInjectsLocalEndpoint(t *testing.T) {
	got := BuildAgentArgs("127.0.0.1:18090", []string{"models"})
	want := []string{"-e", "http://127.0.0.1:18090", "--agent-endpoint", "http://127.0.0.1:18090", "--trust", "models"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildAgentArgs: got %#v want %#v", got, want)
	}
}

func TestBuildAgentArgsRespectsUserOverrides(t *testing.T) {
	got := BuildAgentArgs("http://127.0.0.1:18090", []string{"-e", "http://example.invalid", "--trust", "-p", "hi"})
	want := []string{"--agent-endpoint", "http://127.0.0.1:18090", "-e", "http://example.invalid", "--trust", "-p", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildAgentArgs overrides: got %#v want %#v", got, want)
	}
}

func TestFilterProxyEnvClearsProxyAndSetsCursorEndpoint(t *testing.T) {
	got := FilterProxyEnv([]string{
		"PATH=/usr/bin",
		"HTTPS_PROXY=http://127.0.0.1:9090",
		"http_proxy=http://127.0.0.1:9090",
		"CURSOR_API_ENDPOINT=https://api2.cursor.sh",
		"HOME=/tmp",
	}, "http://127.0.0.1:18090")
	want := []string{
		"PATH=/usr/bin",
		"CURSOR_API_ENDPOINT=http://127.0.0.1:18090",
		"HOME=/tmp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterProxyEnv: got %#v want %#v", got, want)
	}
}

func TestUsageBannerMentionsWrapper(t *testing.T) {
	banner := UsageBanner("127.0.0.1:18090")
	if !strings.Contains(banner, "http://127.0.0.1:18090") || !strings.Contains(banner, "cursor-local-assistant agent -- models") {
		t.Fatalf("unexpected banner: %s", banner)
	}
}

func TestWaitReadyIgnoresProxyEnv(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	if err := WaitReady(server.URL, 2*time.Second); err != nil {
		t.Fatalf("WaitReady should bypass proxy env: %v", err)
	}
}
