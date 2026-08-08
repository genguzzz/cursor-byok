package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cursor/internal/agentcli"
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
	backendOnly        = flag.Bool("backend-only", false, "只启动 18090 backend，不启动 MITM、不注入 Cursor settings")
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "off":
			runOff()
			return
		case "agent":
			os.Exit(agentcli.Run(os.Args[2:], os.Stdout, os.Stderr, os.Stdin))
		}
	}

	flag.Parse()

	if *backendOnly {
		runBackendOnly()
		return
	}

	// 如果使用 debug 端口覆盖，创建临时配置
	if *debugConfigPath != "" || *debugProxyListen != "" || *debugBackendListen != "" {
		runDebugMode()
		return
	}

	logger.Init()
	logger.ApplyDebugLogFromConfig()
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
	fmt.Println(agentcli.UsageBanner(state.BackendListenAddr))
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("Press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nShutting down...")
	// 保留 Cursor 代理设置，便于 ./dev.sh install 热重启后继续拦截；
	// 需要完整恢复账号/设置时请运行: cursor-local-assistant off
	service.ShutdownForQuitPreserveSettings()
	fmt.Println("Stopped (Cursor proxy settings preserved).")
	fmt.Println("Run 'cursor-local-assistant off' to restore Cursor account/settings.")
}

func runBackendOnly() {
	logger.Init()
	logger.ApplyDebugLogFromConfig()
	netproxy.InstallDefaultTransport()

	certManager, err := certs.NewEmbeddedManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cert manager: %v\n", err)
		os.Exit(1)
	}
	service := client.NewProxyService(nil, certManager, certs.EmbeddedCACertPEM())

	fmt.Println("Starting cursor-byok backend-only (no MITM, no Cursor settings inject)...")
	state, err := service.Start(client.StartOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start backend: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("  Backend:   http://%s (%s)\n", state.BackendListenAddr, status(state.BackendRunning))
	fmt.Printf("  Proxy:     skipped\n")
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println(agentcli.UsageBanner(state.BackendListenAddr))
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("Press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nShutting down...")
	service.ShutdownForQuitPreserveSettings()
	fmt.Println("Stopped (Cursor settings untouched).")
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
	logger.ApplyDebugLogFromConfig()
	netproxy.InstallDefaultTransport()

	certManager, err := certs.NewEmbeddedManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cert manager: %v\n", err)
		os.Exit(1)
	}

	caCertPEM := certs.EmbeddedCACertPEM()
	service := client.NewProxyService(nil, certManager, caCertPEM)

	cfg, err := service.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	origProxyListen := cfg.ProxyListenAddr
	origBackendListen := cfg.BackendListenAddr
	overrodePorts := *debugProxyListen != "" || *debugBackendListen != ""

	if *debugProxyListen != "" {
		cfg.ProxyListenAddr = *debugProxyListen
		fmt.Printf("[debug] Overriding proxy listen addr: %s\n", cfg.ProxyListenAddr)
	}
	if *debugBackendListen != "" {
		cfg.BackendListenAddr = *debugBackendListen
		fmt.Printf("[debug] Overriding backend listen addr: %s\n", cfg.BackendListenAddr)
	}

	// 端口覆盖会写共享 config.yaml；必须用 defer 保证异常/信号退出时恢复，避免污染 menubar。
	if overrodePorts {
		if err := service.SaveUserConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save overridden config: %v\n", err)
			os.Exit(1)
		}
		defer restoreDebugPortOverrides(service, *debugProxyListen != "", origProxyListen, *debugBackendListen != "", origBackendListen)
	}

	if *debugConfigPath != "" {
		fmt.Printf("[debug] --config flag is not supported in standalone mode. Use menubar instead.\n")
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
	fmt.Println(agentcli.UsageBanner(state.BackendListenAddr))
	fmt.Println(strings.Repeat("-", 50))
	fmt.Println("[debug mode] Press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nShutting down...")
	// 关闭时不清理 Cursor 设置，避免影响正在运行的 menubar
	service.ShutdownForQuitPreserveSettings()
	fmt.Println("Stopped.")
}

func restoreDebugPortOverrides(service *client.ProxyService, restoreProxy bool, origProxy string, restoreBackend bool, origBackend string) {
	if service == nil || (!restoreProxy && !restoreBackend) {
		return
	}
	cfg, err := service.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config for restore: %v\n", err)
		return
	}
	if restoreProxy {
		cfg.ProxyListenAddr = origProxy
	}
	if restoreBackend {
		cfg.BackendListenAddr = origBackend
	}
	if err := service.SaveUserConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to restore original ports: %v\n", err)
		return
	}
	fmt.Println("Restored original port configuration.")
}

// runOff 清除 Cursor 代理设置并恢复原始账号鉴权，无需启动代理。
func runOff() {
	logger.Init()
	logger.ApplyDebugLogFromConfig()
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
