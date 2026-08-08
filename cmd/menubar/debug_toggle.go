//go:build darwin

// debug_toggle.go 在 menubar 进程内一键管理 cursor-proxy-debugger。
//
// 开启后：
//   - 代理监听 127.0.0.1:9092（避开 Proxyman 常用 9090）
//   - UI 监听 http://127.0.0.1:9091，并自动打开浏览器
//   - 写入 Cursor http.proxy / disableHttp2，并确保本机 CA 已信任
//   - 若本地模式已运行，则 UpstreamProxy=http://127.0.0.1:18080，抓本地协议

package main

import (
	"context"
	"strings"
	"sync"
	"time"

	proxydebugger "cursor/cursor-proxy-debugger"

	"cursor/internal/certs"
	"cursor/internal/cursor"
	"cursor/internal/logger"

	"github.com/pkg/browser"
)

const (
	debugProxyListenAddr = "127.0.0.1:9092"
	debugUIListenAddr    = "127.0.0.1:9091"
	debugProxyURL        = "http://127.0.0.1:9092"
	localModeProxyURL    = "http://127.0.0.1:18080"
)

var debugMu sync.Mutex

var debugState = struct {
	enabled          bool
	server           *proxydebugger.Server
	prevProxyURL     string
	wroteCursorProxy bool
}{}

// toggleDebug 切换调试模式。
func toggleDebug() {
	debugMu.Lock()
	defer debugMu.Unlock()

	if debugState.enabled {
		stopDebugLocked(true)
		return
	}
	startDebugLocked(true)
}

func startDebugLocked(openBrowserTab bool) bool {
	upstream := ""
	if isServiceRunning() {
		upstream = localModeProxyURL
	}

	config := proxydebugger.Config{
		ProxyAddr:     debugProxyListenAddr,
		UIAddr:        debugUIListenAddr,
		TargetHost:    "api2.cursor.sh",
		UpstreamProxy: upstream,
		MaxExchanges:  200,
	}
	server, err := proxydebugger.New(config)
	if err != nil {
		logger.Errorf("menubar: 调试代理创建失败: %v", err)
		updateStatus("状态: 调试启动失败", isServiceRunning(), false)
		return false
	}
	if err := server.Start(); err != nil {
		logger.Errorf("menubar: 调试代理启动失败: %v", err)
		updateStatus("状态: 调试启动失败", isServiceRunning(), false)
		return false
	}

	if err := ensureDebugCATrusted(); err != nil {
		logger.Errorf("menubar: 调试 CA 准备失败: %v", err)
		closeDebugServer(server)
		updateStatus("状态: 调试 CA 失败", isServiceRunning(), false)
		return false
	}

	if !debugState.wroteCursorProxy {
		prevProxy, err := cursor.ReadUserProxyURL()
		if err != nil {
			logger.Errorf("menubar: 读取 Cursor 代理失败: %v", err)
			prevProxy = ""
		}
		if err := cursor.WriteUserProxySettings(debugProxyURL); err != nil {
			logger.Errorf("menubar: 写入 Cursor 调试代理失败: %v", err)
			closeDebugServer(server)
			updateStatus("状态: 调试写代理失败", isServiceRunning(), false)
			return false
		}
		debugState.prevProxyURL = prevProxy
		debugState.wroteCursorProxy = true
	}

	debugState.enabled = true
	debugState.server = server
	logger.Infof("menubar: 调试代理已启动 proxy=%s ui=%s upstream=%q",
		server.ProxyAddr(), server.UIURL(), upstream)

	if openBrowserTab {
		_ = browser.OpenURL(server.UIURL())
	}
	updateStatus("状态: 调试中 "+server.UIURL(), isServiceRunning(), false)
	return true
}

func ensureDebugCATrusted() error {
	caPEM := certs.EmbeddedCACertPEM()
	caPath, err := cursor.EnsureCACertFile(caPEM, "")
	if err != nil {
		return err
	}
	if err := cursor.EnsureCACertInstalled(caPEM, caPath); err != nil {
		return err
	}
	return cursor.SetSystemNodeExtraCACerts(caPath)
}

func closeDebugServer(server *proxydebugger.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = server.Close(ctx)
	cancel()
}

func stopDebugLocked(restoreProxy bool) {
	if debugState.server != nil {
		closeDebugServer(debugState.server)
		debugState.server = nil
	}

	if restoreProxy && debugState.wroteCursorProxy {
		if err := restoreCursorProxyAfterDebug(debugState.prevProxyURL); err != nil {
			logger.Errorf("menubar: 恢复 Cursor 代理失败: %v", err)
		}
		debugState.wroteCursorProxy = false
		debugState.prevProxyURL = ""
		// 本地模式未运行时清掉调试写入的 NODE_EXTRA_CA_CERTS，避免退出后仍信任内置 CA。
		if !isServiceRunning() {
			if err := cursor.ClearSystemNodeExtraCACerts(); err != nil {
				logger.Errorf("menubar: 清理调试 CA env 失败: %v", err)
			}
		}
	}

	if debugState.enabled {
		logger.Infof("menubar: 调试代理已停止")
	}
	debugState.enabled = false

	if !restoreProxy {
		return
	}
	if isServiceRunning() {
		updateStatus("状态: 运行中 (含 Agent CLI)", true, false)
	} else {
		updateStatus("状态: 已停止", false, false)
	}
}

// restoreCursorProxyAfterDebug 在关闭调试后恢复代理：
// - 本地模式仍在跑 → 写回 18080
// - 否则恢复用户原代理；空/调试地址/已失效的本地 MITM 则清除注入项
func restoreCursorProxyAfterDebug(prevProxyURL string) error {
	action, value := decideProxyRestoreAfterDebug(isServiceRunning(), prevProxyURL)
	switch action {
	case proxyRestoreWrite:
		return cursor.WriteUserProxySettings(value)
	case proxyRestoreClear:
		return cursor.ClearUserProxySettings()
	default:
		return nil
	}
}

type proxyRestoreAction int

const (
	proxyRestoreNone proxyRestoreAction = iota
	proxyRestoreWrite
	proxyRestoreClear
)

func decideProxyRestoreAfterDebug(localRunning bool, prevProxyURL string) (proxyRestoreAction, string) {
	if localRunning {
		return proxyRestoreWrite, localModeProxyURL
	}
	prev := strings.TrimSpace(prevProxyURL)
	// 本地模式已停：不要写回无监听的 18080。
	if prev == "" || cursor.IsDebugProxyURL(prev) || cursor.IsLocalAssistantProxyURL(prev) {
		return proxyRestoreClear, ""
	}
	return proxyRestoreWrite, prev
}

// stopDebug 供 quit 等外部路径调用，带锁。
func stopDebug() {
	debugMu.Lock()
	defer debugMu.Unlock()
	stopDebugLocked(true)
}

// isDebugEnabled 返回调试模式是否开启。
func isDebugEnabled() bool {
	debugMu.Lock()
	defer debugMu.Unlock()
	return debugState.enabled
}

// restartDebugLocked 停掉再启调试代理；失败时完整恢复 Cursor 代理，避免悬空指向 9092。
func restartDebugLocked(openBrowserTab bool) {
	logger.Infof("menubar: 重启调试代理以更新 upstream")
	stopDebugLocked(false)
	if startDebugLocked(openBrowserTab) {
		return
	}
	logger.Errorf("menubar: 调试代理重启失败，恢复 Cursor 代理设置")
	stopDebugLocked(true)
}

// refreshDebugUpstream 在本地模式启停后重启调试代理，更新 upstream；不重复弹浏览器。
func refreshDebugUpstream() {
	debugMu.Lock()
	defer debugMu.Unlock()
	if !debugState.enabled && !debugState.wroteCursorProxy {
		return
	}
	restartDebugLocked(false)
}

// ensureDebugProxyAfterLocalStop 关闭本地模式并完成账号恢复后，若调试仍开则继续指向 9092。
func ensureDebugProxyAfterLocalStop() {
	debugMu.Lock()
	defer debugMu.Unlock()
	if !debugState.enabled && !debugState.wroteCursorProxy {
		return
	}
	// 本地模式已完整清理，停调试时不应再写回 18080。
	debugState.prevProxyURL = ""
	if err := cursor.WriteUserProxySettings(debugProxyURL); err != nil {
		logger.Errorf("menubar: 本地模式关闭后重写调试代理失败: %v", err)
		stopDebugLocked(true)
		return
	}
	debugState.wroteCursorProxy = true
	restartDebugLocked(false)
}
