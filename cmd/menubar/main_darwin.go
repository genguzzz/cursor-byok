//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

extern void setupMenubar();
extern void runEventLoop();
extern void stopEventLoop();
extern void updateMenubarStatus(const char *status, int running, int busy);
extern void setProxyMenuItemEnabled(int enabled);
extern void setDebugMenuItemEnabled(int enabled);
*/
import "C"

import (
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"cursor/internal/appdata"
	"cursor/internal/cursor"
	"cursor/internal/logger"
)

const proxyAddr = "http://127.0.0.1:9090"

var (
	actionChan   = make(chan int, 4)
	proxyEnabled bool
	quitOnce     sync.Once
)

//export menuCallback
func menuCallback(action C.int) {
	select {
	case actionChan <- int(action):
	default:
	}
}

func configPath() string {
	return filepath.Join(appdata.RootDir(), "config.yaml")
}

// readProxyEnabled 从 config.yaml 读取 proxy 是否启用。
func readProxyEnabled() bool {
	enabled, err := readProxyEnabledFromFile(configPath())
	if err != nil {
		logger.Errorf("read proxy config failed: %v", err)
		return false
	}
	return enabled
}

// writeProxyConfig 切换 config.yaml 中所有 model adapter 的 proxy 字段。
func writeProxyConfig(enable bool) error {
	return writeProxyConfigToFile(configPath(), enable)
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	logger.Init()

	// 读取初始代理状态。
	proxyEnabled = readProxyEnabled()
	C.setProxyMenuItemEnabled(boolToInt(proxyEnabled))

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigChan {
			switch sig {
			case syscall.SIGTERM:
				// ./dev.sh install 用 SIGTERM 热重启，需保活 Cursor 代理。
				quitAppPreserveSettings()
			case syscall.SIGINT:
				quitApp()
			}
		}
	}()

	C.setupMenubar()
	go warmupProxyService()
	go handleActions()
	// install 重启后：若 Cursor 仍指向本地代理，自动拉起服务，避免必须重启 Cursor。
	go maybeAutoStartLocalMode()
	C.runEventLoop()
}

func handleActions() {
	for action := range actionChan {
		switch action {
		case 1:
			toggleService()
		case 3:
			quitApp()
		case 4:
			toggleProxy()
		case 5:
			forceRestoreCursor()
			C.setDebugMenuItemEnabled(boolToInt(isDebugEnabled()))
		case 6:
			toggleDebug()
			C.setDebugMenuItemEnabled(boolToInt(isDebugEnabled()))
		}
	}
}

func toggleProxy() {
	proxyEnabled = !proxyEnabled
	logger.Infof("toggle proxy -> %v", proxyEnabled)

	if err := writeProxyConfig(proxyEnabled); err != nil {
		logger.Errorf("write proxy config failed: %v", err)
		proxyEnabled = !proxyEnabled // 回滚
		return
	}

	C.setProxyMenuItemEnabled(boolToInt(proxyEnabled))

	// 运行中或启动中都需要热重启，否则 busy 期间改的出站代理要等下次手动开关才生效。
	if isServiceRunning() || isServiceBusy() {
		logger.Infof("restarting service to apply proxy change")
		restartService()
	}
}

func quitApp() {
	quitOnce.Do(func() {
		logger.Infof("menubar: shutting down (full cleanup)")
		// 用户从菜单退出：完整恢复 Cursor 设置/账号，避免留下无监听的 18080/9092。
		stopService(true)
		_ = waitForServiceStop(10 * time.Second)
		stopDebug()
		C.stopEventLoop()
	})
}

// quitAppPreserveSettings 供 install/SIGTERM 热重启：保活本地 MITM 代理，新进程可 maybeAutoStart。
func quitAppPreserveSettings() {
	quitOnce.Do(func() {
		logger.Infof("menubar: shutting down (preserve cursor proxy for hot restart)")
		localWasActive := isServiceRunning() || isServiceBusy()
		shutdownService()

		debugMu.Lock()
		if debugState.server != nil {
			closeDebugServer(debugState.server)
			debugState.server = nil
		}
		debugState.enabled = false
		// 热重启优先保活 18080；纯调试则清掉 9092，避免新进程未开调试时悬空。
		if localWasActive || cursor.IsLocalAssistantProxyURL(debugState.prevProxyURL) {
			if err := cursor.WriteUserProxySettings(localModeProxyURL); err != nil {
				logger.Errorf("menubar: preserve local proxy failed: %v", err)
			}
		} else if debugState.wroteCursorProxy {
			prev := strings.TrimSpace(debugState.prevProxyURL)
			if prev != "" && !cursor.IsDebugProxyURL(prev) && !cursor.IsLocalAssistantProxyURL(prev) {
				if err := cursor.WriteUserProxySettings(prev); err != nil {
					logger.Errorf("menubar: restore user proxy on preserve quit failed: %v", err)
				}
			} else if err := cursor.ClearUserProxySettings(); err != nil {
				logger.Errorf("menubar: clear debug proxy on preserve quit failed: %v", err)
			}
		}
		debugState.wroteCursorProxy = false
		debugState.prevProxyURL = ""
		debugMu.Unlock()

		C.stopEventLoop()
	})
}

func updateStatus(msg string, running bool, busy bool) {
	cMsg := C.CString(msg)
	defer C.free(unsafe.Pointer(cMsg))
	C.updateMenubarStatus(cMsg, boolToInt(running), boolToInt(busy))
}

func boolToInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
