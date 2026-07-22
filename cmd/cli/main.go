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
	"cursor/internal/logger"
	"cursor/internal/netproxy"
)

func main() {
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
	service.ShutdownForQuitPreserveSettings()
	fmt.Println("Stopped.")
}

func status(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
