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
extern void setProxyMenuItemEnabled(int enabled);
*/
import "C"

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

const proxyAddr = "http://127.0.0.1:9090"

var (
	actionChan     = make(chan int, 4)
	serviceMu      sync.Mutex
	cliCmd         *exec.Cmd
	serviceRunning bool
	proxyEnabled   bool
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

func configPath() string {
	return filepath.Join(appdata.RootDir(), "config.yaml")
}

// readProxyEnabled 从 config.yaml 读取 proxy 是否启用
func readProxyEnabled() bool {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "proxy: "+proxyAddr)
}

// writeProxyConfig 切换 config.yaml 中所有 model adapter 的 proxy 字段
func writeProxyConfig(enable bool) error {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	if enable {
		content = strings.ReplaceAll(content, `proxy: ""`, "proxy: "+proxyAddr)
	} else {
		content = strings.ReplaceAll(content, "proxy: "+proxyAddr, `proxy: ""`)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	logger.Init()

	// 读取初始代理状态
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
		case 4:
			toggleProxy()
		}
	}
}

func toggleProxy() {
	proxyEnabled = !proxyEnabled
	logger.Infof("toggle proxy → %v", proxyEnabled)

	if err := writeProxyConfig(proxyEnabled); err != nil {
		logger.Errorf("write proxy config failed: %v", err)
		proxyEnabled = !proxyEnabled // 回滚
		return
	}

	C.setProxyMenuItemEnabled(boolToInt(proxyEnabled))

	// 如果 CLI 正在运行，重启以应用新的代理设置
	serviceMu.Lock()
	running := serviceRunning
	cmd := cliCmd
	serviceMu.Unlock()

	if running && cmd != nil && cmd.Process != nil {
		logger.Infof("restarting CLI to apply proxy change")
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		// 等待端口释放
		// 直接重新启动
		startInterception()
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
	cmd.Stdin = nil
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
	C.updateMenubarStatus(cstr, boolToInt(running))
}

func boolToInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
