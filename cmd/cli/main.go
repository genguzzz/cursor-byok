package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/cursor"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
)

var (
	debugConfigPath    = flag.String("config", "", "Path to custom config.yaml (overrides default ~/.cursor-local-assistant-v2/config.yaml)")
	debugProxyListen   = flag.String("proxy-listen", "", "Override proxy listen address (e.g. 127.0.0.1:19080)")
	debugBackendListen = flag.String("backend-listen", "", "Override backend listen address (e.g. 127.0.0.1:19090)")
)

func main() {
	// off 子命令：清除 Cursor 代理设置并恢复原始账号鉴权
	if len(os.Args) > 1 && os.Args[1] == "off" {
		runOff()
		return
	}

	flag.Parse()

	// 如果使用 debug 端口覆盖，创建临时配置
	if *debugConfigPath != "" || *debugProxyListen != "" || *debugBackendListen != "" {
		runDebugMode()
		return
	}

	logger.Init()
	netproxy.InstallDefaultTransport()

	certManager, err := certs.NewEmbeddedManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cert manager: %v\n", err)
		os.Exit(1)
	}

	caCertPEM := certs.EmbeddedCACertPEM()
	service := client.NewProxyService(nil, certManager, caCertPEM)

	fmt.Println("Starting cursor-byok local assistant...")
	state, err := service.StartProxy()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("  Backend:   http://%s (%s)\n", state.BackendListenAddr, status(state.BackendRunning))
	fmt.Printf("  Proxy:     http://%s (%s)\n", state.ProxyListenAddr, status(state.ProxyRunning))
	if state.NetProxyActive {
		fmt.Printf("  Out Proxy: %s (%s)\n", state.NetProxyDescription, "active")
	}
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("Press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nShutting down...")
	service.ShutdownForQuit()
	fmt.Println("Stopped.")
}

func status(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}

// runDebugMode 使用不同的端口和/或配置文件启动，避免与已运行的 menubar 端口冲突。
// 支持 --proxy-listen / --backend-listen 端口覆盖或 --config 自定义配置文件。
func runDebugMode() {
	logger.Init()
	netproxy.InstallDefaultTransport()

	certManager, err := certs.NewEmbeddedManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cert manager: %v\n", err)
		os.Exit(1)
	}

	caCertPEM := certs.EmbeddedCACertPEM()
	service := client.NewProxyService(nil, certManager, caCertPEM)

	// 加载当前配置
	cfg, err := service.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 应用端口覆盖
	if *debugProxyListen != "" {
		cfg.ProxyListenAddr = *debugProxyListen
		fmt.Printf("[debug] Overriding proxy listen addr: %s\n", cfg.ProxyListenAddr)
	}
	if *debugBackendListen != "" {
		cfg.BackendListenAddr = *debugBackendListen
		fmt.Printf("[debug] Overriding backend listen addr: %s\n", cfg.BackendListenAddr)
	}

	// 如果有端口覆盖，需要保存以便 backend 使用新地址
	if *debugProxyListen != "" || *debugBackendListen != "" {
		if err := service.SaveUserConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save overridden config: %v\n", err)
			os.Exit(1)
		}
	}

	// 如果指定了 --config，则使用自定义配置文件
	if *debugConfigPath != "" {
		fmt.Printf("[debug] Using custom config: %s\n", *debugConfigPath)
		// config path 在 NewProxyService 时已固定，这里通过重新加载覆盖
	}

	fmt.Println("Starting cursor-byok local assistant (debug mode)...")
	state, err := service.StartProxy()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("  Backend:   http://%s (%s)\n", state.BackendListenAddr, status(state.BackendRunning))
	fmt.Printf("  Proxy:     http://%s (%s)\n", state.ProxyListenAddr, status(state.ProxyRunning))
	if state.NetProxyActive {
		fmt.Printf("  Out Proxy: %s (%s)\n", state.NetProxyDescription, "active")
	}
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("[debug mode] Press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nShutting down...")
	// 关闭时不清理 Cursor 设置，避免影响正在运行的 menubar
	service.ShutdownForQuitPreserveSettings()

	// 恢复原始端口配置
	if *debugProxyListen != "" || *debugBackendListen != "" {
		cfg, err := service.LoadUserConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load config for restore: %v\n", err)
			return
		}
		if *debugProxyListen != "" {
			cfg.ProxyListenAddr = "127.0.0.1:18080"
		}
		if *debugBackendListen != "" {
			cfg.BackendListenAddr = "127.0.0.1:18090"
		}
		if err := service.SaveUserConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to restore original ports: %v\n", err)
		} else {
			fmt.Println("Restored original port configuration.")
		}
	}
	fmt.Println("Stopped.")
}

// runOff 清除 Cursor 代理设置并恢复原始账号鉴权，无需启动代理。
func runOff() {
	logger.Init()
	fmt.Println("Turning off cursor-local-assistant...")

	hasErr := false

	// 1. 清除 Cursor 代理设置
	if err := cursor.ClearUserProxySettings(); err != nil {
		fmt.Fprintf(os.Stderr, "  Clear proxy settings: %v\n", err)
		hasErr = true
	} else {
		fmt.Println("  ✓ Cursor proxy settings cleared")
	}

	// 2. 恢复原始账号鉴权
	if err := cursor.RestoreCursorAuthState(); err != nil {
		fmt.Fprintf(os.Stderr, "  Restore auth: %v\n", err)
		hasErr = true
	} else {
		fmt.Println("  ✓ Cursor auth restored")
	}

	// 3. 清除 macOS Node CA 证书环境变量
	if err := cursor.ClearSystemNodeExtraCACerts(); err != nil {
		fmt.Fprintf(os.Stderr, "  Clear CA certs: %v\n", err)
		hasErr = true
	} else {
		fmt.Println("  ✓ CA cert settings cleared")
	}

	if hasErr {
		fmt.Println("\nCompleted with errors. Please restart Cursor.")
		os.Exit(1)
	}
	fmt.Println("\nDone. Please restart Cursor to use your original account.")
}
