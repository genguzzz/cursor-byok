// tclaude_daemon.go 实现 tclaude daemon 端口的动态解析。
//
// tclaude 每次重启后监听端口会变化，端口记录在 ~/.tclaude/daemon.json 中。
// 当模型适配器的 baseURL 使用特殊主机名 tclaude-daemon 时（例如
// http://tclaude-daemon/v1/messages?beta=true），resolver 会读取 daemon.json
// 获取当前实际端口，并将主机名替换为 127.0.0.1:<port>。
package config

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cursor/internal/logger"
)

// tclaudeDaemonHost 是 config.yaml 中用于触发动态端口解析的特殊主机名。
const tclaudeDaemonHost = "tclaude-daemon"

// tclaudeDaemonEntry 对应 ~/.tclaude/daemon.json 的结构。
type tclaudeDaemonEntry struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
}

// resolveTclaudeDaemonBaseURL 检测 baseURL 是否使用 tclaude-daemon 主机名，
// 若是则读取 ~/.tclaude/daemon.json 获取当前端口并替换主机名。
// 若无法读取 daemon.json 或 baseURL 未使用特殊主机名，则原样返回。
func resolveTclaudeDaemonBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return baseURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return baseURL
	}
	if !strings.EqualFold(parsed.Hostname(), tclaudeDaemonHost) {
		return baseURL
	}
	daemon, err := readTclaudeDaemonEntry()
	if err != nil {
		logger.Errorf("resolve tclaude daemon url failed: %v, using raw baseURL", err)
		return baseURL
	}
	port := daemon.Port
	if port <= 0 && daemon.URL != "" {
		if daemonURL, parseErr := url.Parse(daemon.URL); parseErr == nil {
			if portStr := daemonURL.Port(); portStr != "" {
				if p, convErr := strconv.Atoi(portStr); convErr == nil {
					port = p
				}
			}
		}
	}
	if port <= 0 {
		logger.Errorf("tclaude daemon.json has no valid port, using raw baseURL")
		return baseURL
	}
	parsed.Host = "127.0.0.1:" + strconv.Itoa(port)
	parsed.Scheme = "http"
	resolved := parsed.String()
	logger.Infof("resolved tclaude daemon baseURL: %s", resolved)
	return resolved
}

// readTclaudeDaemonEntry 读取 ~/.tclaude/daemon.json。
func readTclaudeDaemonEntry() (*tclaudeDaemonEntry, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	daemonPath := filepath.Join(homeDir, ".tclaude", "daemon.json")
	data, err := os.ReadFile(daemonPath)
	if err != nil {
		return nil, err
	}
	var entry tclaudeDaemonEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}
