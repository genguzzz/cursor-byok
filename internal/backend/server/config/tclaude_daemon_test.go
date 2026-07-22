package config

import (
	"testing"
)

// TestResolveTclaudeDaemonBaseURLPassthrough 验证非 tclaude-daemon 主机名的 URL 原样返回。
func TestResolveTclaudeDaemonBaseURLPassthrough(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"normal http url", "http://127.0.0.1:8080/v1/messages?beta=true"},
		{"https url", "https://api.anthropic.com/v1/messages"},
		{"empty string", ""},
		{"localhost", "http://localhost:3000/v1/messages"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveTclaudeDaemonBaseURL(tc.url)
			if got != tc.url {
				t.Errorf("resolveTclaudeDaemonBaseURL(%q) = %q, want %q (passthrough)", tc.url, got, tc.url)
			}
		})
	}
}

// TestResolveTclaudeDaemonBaseURLWithDaemon 验证当 daemon.json 存在时，
// tclaude-daemon 主机名会被替换为 127.0.0.1:<port>。
// 此测试依赖本机 ~/.tclaude/daemon.json 文件存在；若不存在则跳过。
func TestResolveTclaudeDaemonBaseURLWithDaemon(t *testing.T) {
	daemon, err := readTclaudeDaemonEntry()
	if err != nil {
		t.Skipf("skipping: cannot read ~/.tclaude/daemon.json: %v", err)
	}
	if daemon.Port <= 0 {
		t.Skipf("skipping: daemon.json has no valid port")
	}

	input := "http://tclaude-daemon/v1/messages?beta=true"
	got := resolveTclaudeDaemonBaseURL(input)
	if got == input {
		t.Fatalf("URL was not resolved, still %q", got)
	}
	// 验证解析后的 URL 包含 127.0.0.1 和正确的端口
	if !contains(got, "127.0.0.1") {
		t.Errorf("resolved URL should contain 127.0.0.1, got: %q", got)
	}
	if !contains(got, "/v1/messages?beta=true") {
		t.Errorf("resolved URL should preserve path and query, got: %q", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
