//go:build darwin

// service_lifecycle.go 在 menubar 进程内直接管理 ProxyService 生命周期。

package main

import (
	"fmt"
	"sync"
	"time"

	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/cursor"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
)

var (
	serviceMu sync.Mutex

	serviceState = struct {
		running     bool
		busy        bool
		clearOnStop bool
		stopCh      chan struct{}
		stopOnce    *sync.Once
		doneCh      chan struct{}
	}{}

	sharedProxyOnce sync.Once
	sharedProxySvc  *client.ProxyService
	sharedProxyErr  error
)

// warmupProxyService 在 menubar 启动后后台预热 ProxyService（含 backend mux 构建）。
func warmupProxyService() {
	go func() {
		if _, err := getSharedProxyService(); err != nil {
			logger.Errorf("menubar: proxy warmup failed: %v", err)
			return
		}
		logger.Infof("menubar: proxy service warmed up")
	}()
}

func getSharedProxyService() (*client.ProxyService, error) {
	sharedProxyOnce.Do(func() {
		netproxy.InstallDefaultTransport()
		certManager, err := certs.NewEmbeddedManager()
		if err != nil {
			sharedProxyErr = fmt.Errorf("证书管理器初始化失败: %w", err)
			return
		}
		sharedProxySvc = client.NewProxyService(nil, certManager, certs.EmbeddedCACertPEM())
	})
	return sharedProxySvc, sharedProxyErr
}

// startService 在当前进程内启动 ProxyService（复用单例，避免每次重建 backend）。
func startService() {
	serviceMu.Lock()
	if serviceState.running || serviceState.busy {
		serviceMu.Unlock()
		return
	}
	prevDone := serviceState.doneCh
	serviceMu.Unlock()

	if prevDone != nil {
		select {
		case <-prevDone:
		case <-time.After(25 * time.Second):
			logger.Errorf("menubar: 等待上一轮服务退出超时")
			return
		}
	}

	serviceMu.Lock()
	if serviceState.running || serviceState.busy {
		serviceMu.Unlock()
		return
	}
	serviceState.busy = true
	serviceState.clearOnStop = false
	serviceState.stopCh = make(chan struct{})
	serviceState.stopOnce = &sync.Once{}
	serviceState.doneCh = make(chan struct{})
	stopCh := serviceState.stopCh
	doneCh := serviceState.doneCh
	serviceMu.Unlock()

	updateStatus("状态: 启动中...", false, true)

	svc, err := getSharedProxyService()
	if err != nil {
		logger.Errorf("menubar: %v", err)
		resetServiceStateLocked()
		updateStatus("状态: 启动失败", false, false)
		return
	}

	go func() {
		defer func() {
			serviceMu.Lock()
			serviceState.running = false
			serviceState.busy = false
			serviceState.stopCh = nil
			serviceState.stopOnce = nil
			if serviceState.doneCh == doneCh {
				close(doneCh)
			}
			serviceState.doneCh = nil
			serviceMu.Unlock()
		}()

		logger.Infof("menubar: starting proxy service")
		state, err := svc.StartProxy()
		if err != nil {
			logger.Errorf("menubar: StartProxy 失败: %v", err)
			updateStatus("状态: 启动失败", false, false)
			return
		}

		serviceMu.Lock()
		serviceState.running = true
		serviceState.busy = false
		serviceMu.Unlock()
		logger.Infof("menubar: proxy running on %s, backend on %s",
			state.ProxyListenAddr, state.BackendListenAddr)
		updateStatus("状态: 运行中", true, false)
		refreshDebugUpstream()

		<-stopCh

		updateStatus("状态: 关闭中...", true, true)
		clearSettings := takeClearOnStop()
		shutdownProxy(svc, clearSettings)
		logger.Infof("menubar: 服务已停止 clear_settings=%v", clearSettings)
		updateStatus("状态: 已停止", false, false)

		if clearSettings {
			ensureDebugProxyAfterLocalStop()
		}
	}()
}

func resetServiceStateLocked() {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	serviceState.busy = false
	serviceState.stopCh = nil
	serviceState.stopOnce = nil
	serviceState.doneCh = nil
}

func takeClearOnStop() bool {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	clear := serviceState.clearOnStop
	serviceState.clearOnStop = false
	return clear
}

func shutdownProxy(svc *client.ProxyService, clearSettings bool) {
	if svc == nil {
		return
	}
	if clearSettings {
		svc.ShutdownForQuit()
		return
	}
	svc.ShutdownForQuitPreserveSettings()
}

// stopService 停止进程内的 ProxyService。
func stopService(clearSettings bool) {
	serviceMu.Lock()
	stopCh := serviceState.stopCh
	stopOnce := serviceState.stopOnce
	doneCh := serviceState.doneCh
	wasRunning := serviceState.running
	runningOrBusy := serviceState.running || serviceState.busy
	if !runningOrBusy || stopCh == nil || stopOnce == nil || doneCh == nil {
		serviceMu.Unlock()
		return
	}
	if clearSettings {
		serviceState.clearOnStop = true
	}
	serviceMu.Unlock()

	if wasRunning {
		updateStatus("状态: 关闭中...", true, true)
	}
	stopOnce.Do(func() { close(stopCh) })

	select {
	case <-doneCh:
	case <-time.After(25 * time.Second):
		logger.Errorf("menubar: 服务停止超时")
	}
}

func isServiceRunning() bool {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	return serviceState.running
}

func isServiceBusy() bool {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	return serviceState.busy
}

// toggleService 切换本地模式。关闭时完整恢复 Cursor 设置与原账号。
func toggleService() {
	if isServiceBusy() {
		return
	}
	if isServiceRunning() {
		stopService(true)
		return
	}
	startService()
}

// restartService 保留 Cursor 设置的热重启（切换出站代理等）。
func restartService() {
	if !isServiceRunning() && !isServiceBusy() {
		return
	}
	logger.Infof("menubar: restarting service")
	stopService(false)
	_ = waitForServiceStop(25 * time.Second)
	startService()
}

// forceRestoreCursor 强制清理：停本地模式并恢复账号/设置（异常残留时用）。
func forceRestoreCursor() {
	updateStatus("状态: 恢复账号中...", false, true)
	stopDebug()
	if isServiceRunning() || isServiceBusy() {
		stopService(true)
		_ = waitForServiceStop(25 * time.Second)
	} else {
		if err := cursor.RestoreCursorAuthState(); err != nil {
			logger.Errorf("menubar: restore cursor auth failed: %v", err)
			updateStatus("状态: 恢复账号失败", false, false)
			return
		}
		if err := cursor.ClearUserProxySettings(); err != nil {
			logger.Errorf("menubar: clear cursor proxy failed: %v", err)
			updateStatus("状态: 恢复设置失败", false, false)
			return
		}
		if err := cursor.ClearSystemNodeExtraCACerts(); err != nil {
			logger.Errorf("menubar: clear CA env failed: %v", err)
		}
	}
	logger.Infof("menubar: cursor auth/settings restored")
	updateStatus("状态: 已恢复账号", false, false)
}

func waitForServiceStop(timeout time.Duration) error {
	serviceMu.Lock()
	doneCh := serviceState.doneCh
	serviceMu.Unlock()

	if doneCh == nil {
		return nil
	}

	select {
	case <-doneCh:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for service stop")
	}
}

func shutdownService() {
	stopService(false)
	_ = waitForServiceStop(10 * time.Second)
}

// maybeAutoStartLocalMode 若 Cursor 仍指向本地 MITM（install 热重启后），自动拉起。
func maybeAutoStartLocalMode() {
	time.Sleep(300 * time.Millisecond)
	if isServiceRunning() || isServiceBusy() {
		return
	}
	proxyURL, err := cursor.ReadUserProxyURL()
	if err != nil {
		logger.Errorf("menubar: 读取 Cursor 代理失败: %v", err)
		return
	}
	if cursor.IsLocalAssistantProxyURL(proxyURL) {
		logger.Infof("menubar: detected local MITM proxy=%s, auto-starting", proxyURL)
		startService()
	}
}
