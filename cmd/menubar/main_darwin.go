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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"cursor/internal/logger"
)

var (
	actionChan     = make(chan int, 4)
	serviceMu      sync.Mutex
	cliCmd         *exec.Cmd
	serviceRunning bool
)

//export menuCallback
func menuCallback(action C.int) {
	select {
	case actionChan <- int(action):
	default:
	}
}

// findCLIBinary 查找 CLI 二进制文件路径
func findCLIBinary() string {
	// 1. 与当前二进制同目录
	exePath, err := os.Executable()
	if err == nil {
		p := filepath.Join(filepath.Dir(exePath), "cursor-local-assistant")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// 2. .app bundle 内
	if exePath, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exePath), "cursor-local-assistant")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// 3. 项目根目录（开发模式）
	cwd, err := os.Getwd()
	if err == nil {
		p := filepath.Join(cwd, "cursor-local-assistant")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	logger.Init()

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
	serviceMu.Unlock()

	updateStatus("状态: 启动中...", false)

	cliPath := findCLIBinary()
	if cliPath == "" {
		logger.Errorf("CLI binary not found")
		updateStatus("状态: 找不到CLI", false)
		return
	}

	cmd := exec.Command(cliPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Errorf("start CLI failed: %v", err)
		updateStatus("状态: 启动失败", false)
		return
	}

	serviceMu.Lock()
	cliCmd = cmd
	serviceRunning = true
	serviceMu.Unlock()

	updateStatus("状态: 运行中", true)

	// 等待子进程退出（异常退出时自动更新状态）
	go func() {
		_ = cmd.Wait()
		serviceMu.Lock()
		if cliCmd == cmd {
			serviceRunning = false
			cliCmd = nil
		}
		serviceMu.Unlock()
		updateStatus("状态: 已停止", false)
	}()
}

func stopInterception() {
	serviceMu.Lock()
	if !serviceRunning {
		serviceMu.Unlock()
		return
	}
	cmd := cliCmd
	serviceMu.Unlock()

	updateStatus("状态: 关闭中...", true)

	// 发 SIGTERM 让 CLI 优雅退出（ShutdownForQuit 恢复账号）
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}

	serviceMu.Lock()
	serviceRunning = false
	cliCmd = nil
	serviceMu.Unlock()
	updateStatus("状态: 已停止", false)
}

func quitApp() {
	serviceMu.Lock()
	cmd := cliCmd
	running := serviceRunning
	serviceMu.Unlock()

	if running && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
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
