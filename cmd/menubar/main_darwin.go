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
	"sync"
	"syscall"
	"unsafe"

	"cursor/internal/appdata"
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
			if sig == syscall.SIGTERM {
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

	// 如果服务正在运行，热重启以应用新的代理设置。
	if isServiceRunning() {
		logger.Infof("restarting service to apply proxy change")
		restartService()
	}
}

func quitApp() {
	quitOnce.Do(func() {
		logger.Infof("menubar: shutting down")
		shutdownService()
		stopDebug()
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
