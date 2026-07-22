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
	"os/signal"
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

func findCLIBinary() string {
	exePath, err := os.Executable()
	if err == nil {
		p := filepath.Join(filepath.Dir(exePath), "cursor-local-assistant")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
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

	// 菜单栏程序忽略 SIGINT（Ctrl+C），只能通过菜单"退出"关闭
	// 防止 CLI 子进程的信号传播到菜单栏程序导致意外退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigChan {
			if sig == syscall.SIGTERM {
				quitApp()
			}
			// SIGINT 被忽略，防止终端 Ctrl+C 杀掉菜单栏程序
		}
	}()

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

	// 打开日志文件作为子进程的 stdout/stderr
	logPath := filepath.Join(os.Getenv("HOME"), ".cursor-local-assistant-v2", "logs", "cli-subprocess.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logFile = os.Stderr
	} else {
		defer logFile.Close()
	}

	cmd := exec.Command(cliPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil // 不继承终端 stdin
	// 独立进程组，防止终端信号传播到子进程
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logger.Errorf("start CLI failed: %v", err)
		updateStatus("状态: 启动失败", false)
		return
	}

	logger.Infof("CLI subprocess started pid=%d path=%s", cmd.Process.Pid, cliPath)

	serviceMu.Lock()
	cliCmd = cmd
	serviceRunning = true
	serviceMu.Unlock()

	updateStatus("状态: 运行中", true)

	// 监控子进程退出
	go func() {
		_ = cmd.Wait()
		logger.Infof("CLI subprocess exited pid=%d", cmd.Process.Pid)
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

	if cmd != nil && cmd.Process != nil {
		// 发 SIGTERM 让 CLI 优雅退出（ShutdownForQuit 恢复账号）
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
