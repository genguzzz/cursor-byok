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

func main() {
	// off 子命令：清除 Cursor 代理设置并恢复原始账号鉴权
	if len(os.Args) > 1 && os.Args[1] == "off" {
		runOff()
		return
	}

	flag.Parse()

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
