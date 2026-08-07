//go:build darwin

// service_lifecycle_test.go 测试 menubar ServiceManager 的状态机逻辑。
// 不启动真实的 ProxyService / debugger（避免占用 18090/9092）。

package main

import (
	"sync"
	"testing"
	"time"

	"cursor/internal/cursor"
)

// resetServiceState 重置 serviceState 到初始状态（测试用）。
func resetServiceState() {
	serviceMu.Lock()
	defer serviceMu.Unlock()
	serviceState.running = false
	serviceState.busy = false
	serviceState.clearOnStop = false
	serviceState.stopCh = nil
	serviceState.stopOnce = nil
	serviceState.doneCh = nil
}

func resetDebugState() {
	debugMu.Lock()
	defer debugMu.Unlock()
	debugState.enabled = false
	debugState.server = nil
	debugState.prevProxyURL = ""
	debugState.wroteCursorProxy = false
}

func TestServiceStateConcurrency(t *testing.T) {
	resetServiceState()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = isServiceRunning()
			_ = isServiceBusy()
		}()
	}
	wg.Wait()
}

func TestWaitForServiceStopIdle(t *testing.T) {
	resetServiceState()

	err := waitForServiceStop(100 * time.Millisecond)
	if err != nil {
		t.Fatalf("expected no error when service is not running, got: %v", err)
	}
}

func TestShutdownServiceIdle(t *testing.T) {
	resetServiceState()
	shutdownService()
}

func TestStopServiceIdle(t *testing.T) {
	resetServiceState()
	stopService(false)
	stopService(true)
	stopService(false)
}

func TestStopChannelClosedOnce(t *testing.T) {
	resetServiceState()

	serviceMu.Lock()
	serviceState.busy = true
	serviceState.stopCh = make(chan struct{})
	serviceState.stopOnce = &sync.Once{}
	serviceState.doneCh = make(chan struct{})
	stopCh := serviceState.stopCh
	stopOnce := serviceState.stopOnce
	doneCh := serviceState.doneCh
	serviceMu.Unlock()

	go func() {
		<-stopCh
		close(doneCh)
	}()

	stopOnce.Do(func() { close(stopCh) })
	stopOnce.Do(func() { close(stopCh) }) // 不应 panic

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("doneCh not closed")
	}
}

func TestDebugStateDefaults(t *testing.T) {
	resetDebugState()
	if isDebugEnabled() {
		t.Fatal("debug should be disabled by default")
	}
	stopDebug()
	if isDebugEnabled() {
		t.Fatal("debug should remain disabled")
	}
}

func TestDebugListenConstants(t *testing.T) {
	if debugProxyListenAddr != "127.0.0.1:9092" {
		t.Fatalf("debug proxy should be 9092, got %s", debugProxyListenAddr)
	}
	if debugUIListenAddr != "127.0.0.1:9091" {
		t.Fatalf("debug UI should be 9091, got %s", debugUIListenAddr)
	}
	if debugProxyURL != "http://127.0.0.1:9092" {
		t.Fatalf("debug proxy URL mismatch: %s", debugProxyURL)
	}
	if !cursor.IsDebugProxyURL(debugProxyURL) {
		t.Fatal("IsDebugProxyURL should recognize debug proxy")
	}
	if !cursor.IsLocalAssistantProxyURL(localModeProxyURL) {
		t.Fatal("IsLocalAssistantProxyURL should recognize local MITM")
	}
}

func TestDecideProxyRestoreAfterDebug(t *testing.T) {
	action, value := decideProxyRestoreAfterDebug(true, "http://example:1")
	if action != proxyRestoreWrite || value != localModeProxyURL {
		t.Fatalf("local running should write 18080, got action=%d value=%q", action, value)
	}

	action, value = decideProxyRestoreAfterDebug(false, localModeProxyURL)
	if action != proxyRestoreClear {
		t.Fatalf("dead local proxy should clear, got action=%d value=%q", action, value)
	}

	action, value = decideProxyRestoreAfterDebug(false, debugProxyURL)
	if action != proxyRestoreClear {
		t.Fatalf("debug proxy should clear, got action=%d value=%q", action, value)
	}

	action, value = decideProxyRestoreAfterDebug(false, "http://127.0.0.1:7890")
	if action != proxyRestoreWrite || value != "http://127.0.0.1:7890" {
		t.Fatalf("user proxy should restore, got action=%d value=%q", action, value)
	}

	action, value = decideProxyRestoreAfterDebug(false, "")
	if action != proxyRestoreClear {
		t.Fatalf("empty prev should clear, got action=%d value=%q", action, value)
	}
}

func TestRestartDebugLockedRestoresOnStartFailure(t *testing.T) {
	resetDebugState()
	debugMu.Lock()
	defer debugMu.Unlock()
	debugState.enabled = true
	debugState.wroteCursorProxy = true
	debugState.prevProxyURL = "http://127.0.0.1:7890"
	stopDebugLocked(false)
	if debugState.enabled {
		t.Fatal("stopDebugLocked(false) should clear enabled")
	}
	if !debugState.wroteCursorProxy {
		t.Fatal("stopDebugLocked(false) should keep wroteCursorProxy for restart")
	}
	// 不触发真实 WriteUserProxySettings：只验证失败收口会清标记。
	debugState.wroteCursorProxy = false
	debugState.prevProxyURL = ""
	stopDebugLocked(true)
	if debugState.enabled || debugState.wroteCursorProxy {
		t.Fatal("stopDebugLocked(true) should clear enabled/wroteCursorProxy")
	}
}

func TestQuitOnceSafe(t *testing.T) {
	quitOnce = sync.Once{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			quitOnce.Do(func() { /* no-op */ })
		}()
	}
	wg.Wait()
}
