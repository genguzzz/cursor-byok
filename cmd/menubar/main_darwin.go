//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

extern void setupMenubar();
extern void runEventLoop();
extern void stopEventLoop();
extern void updateMenubarStatus(const char *status, int running);
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
)

var (
	actionChan     = make(chan int, 4)
	proxyService   *client.ProxyService
	serviceMu      sync.Mutex
	serviceRunning bool
)

//export menuCallback
func menuCallback(action C.int) {
	select {
	case actionChan <- int(action):
	default:
	}
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	logger.Init()
	netproxy.InstallDefaultTransport()

	certManager, err := certs.NewEmbeddedManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cert manager: %v\n", err)
		os.Exit(1)
	}
	caCertPEM := certs.EmbeddedCACertPEM()
	proxyService = client.NewProxyService(nil, certManager, caCertPEM)

	C.setupMenubar()
	go handleActions()
	C.runEventLoop()
}

func handleActions() {
	for action := range actionChan {
		switch action {
		case 1:
			startInterception()
		case 2:
			stopInterception()
		case 3:
			quitApp()
		}
	}
}

func startInterception() {
	serviceMu.Lock()
	if serviceRunning {
		serviceMu.Unlock()
		return
	}
	serviceRunning = true
	serviceMu.Unlock()

	updateStatus("状态: 启动中...", false)

	_, err := proxyService.StartProxy()
	if err != nil {
		logger.Errorf("start proxy failed: %v", err)
		serviceMu.Lock()
		serviceRunning = false
		serviceMu.Unlock()
		updateStatus("状态: 启动失败", false)
		return
	}
	updateStatus("状态: 运行中", true)
}

func stopInterception() {
	serviceMu.Lock()
	if !serviceRunning {
		serviceMu.Unlock()
		return
	}
	serviceMu.Unlock()

	updateStatus("状态: 关闭中...", true)

	proxyService.ShutdownForQuit()

	serviceMu.Lock()
	serviceRunning = false
	serviceMu.Unlock()
	updateStatus("状态: 已停止", false)
}

func quitApp() {
	serviceMu.Lock()
	running := serviceRunning
	serviceMu.Unlock()

	if running {
		proxyService.ShutdownForQuit()
	}
	C.stopEventLoop()
}

func updateStatus(status string, running bool) {
	cstr := C.CString(status)
	defer C.free(unsafe.Pointer(cstr))
	runVal := C.int(0)
	if running {
		runVal = 1
	}
	C.updateMenubarStatus(cstr, runVal)
}
