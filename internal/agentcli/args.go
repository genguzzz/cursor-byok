package agentcli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"

	"gopkg.in/yaml.v3"
)

const DefaultListenAddr = serverconfig.DefaultBackendListenAddr

var proxyEnvKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// BackendBaseURL 把 listen addr 收成 CLI 可用的 http URL。
func BackendBaseURL(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	switch {
	case addr == "":
		addr = DefaultListenAddr
	case strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://"):
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr
}

// LoadBackendBaseURL 读取用户 config.yaml 的 backendListenAddr；失败则回退默认 18090。
func LoadBackendBaseURL() string {
	path := appdata.ConfigFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return BackendBaseURL("")
	}
	var parsed struct {
		BackendListenAddr string `yaml:"backendListenAddr"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return BackendBaseURL("")
	}
	return BackendBaseURL(parsed.BackendListenAddr)
}

// BuildAgentArgs 在用户参数前注入本地 endpoint；用户已显式传入的同名 flag 不会重复。
func BuildAgentArgs(backendURL string, userArgs []string) []string {
	baseURL := BackendBaseURL(backendURL)
	injected := make([]string, 0, 6)
	if !hasFlag(userArgs, "-e", "--endpoint") {
		injected = append(injected, "-e", baseURL)
	}
	if !hasFlag(userArgs, "--agent-endpoint") {
		injected = append(injected, "--agent-endpoint", baseURL)
	}
	if !hasFlag(userArgs, "--trust") {
		injected = append(injected, "--trust")
	}
	return append(injected, userArgs...)
}

func hasFlag(args []string, names ...string) bool {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if _, ok := wanted[name]; ok {
			return true
		}
	}
	return false
}

// FilterProxyEnv 去掉会把 localhost 拐走的代理变量，并写入 CURSOR_API_ENDPOINT。
func FilterProxyEnv(parent []string, backendURL string) []string {
	blocked := make(map[string]struct{}, len(proxyEnvKeys))
	for _, key := range proxyEnvKeys {
		blocked[key] = struct{}{}
	}
	out := make([]string, 0, len(parent)+1)
	seenCursorEndpoint := false
	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, drop := blocked[key]; drop {
			continue
		}
		if key == "CURSOR_API_ENDPOINT" {
			seenCursorEndpoint = true
			out = append(out, "CURSOR_API_ENDPOINT="+BackendBaseURL(backendURL))
			continue
		}
		out = append(out, entry)
	}
	if !seenCursorEndpoint {
		out = append(out, "CURSOR_API_ENDPOINT="+BackendBaseURL(backendURL))
	}
	return out
}

// LookUpAgent 查找官方 Cursor CLI `agent` 可执行文件。
func LookUpAgent() (string, error) {
	path, err := exec.LookPath("agent")
	if err != nil {
		return "", fmt.Errorf("未找到 agent 可执行文件，请先安装 Cursor CLI: %w", err)
	}
	return path, nil
}

// UsageBanner 返回本地模式启动后给用户看的 Agent CLI 用法。
func UsageBanner(backendURL string) string {
	baseURL := BackendBaseURL(backendURL)
	return fmt.Sprintf(
		"Agent CLI 本地模式已就绪（%s，不走官方 api5）\n  cursor-local-assistant agent -- models\n  cursor-local-assistant agent -- -p \"Reply with exactly: LOCAL-OK\" --mode ask --output-format json",
		baseURL,
	)
}
