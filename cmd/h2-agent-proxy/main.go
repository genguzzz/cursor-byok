package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	h2agentproxy "cursor/h2-agent-proxy"
	"cursor/internal/certs"
)

func main() {
	config := h2agentproxy.Config{}
	decodeDir := ""
	flag.StringVar(&config.ListenAddr, "listen", "127.0.0.1:8443", "TLS+h2 listen address")
	flag.StringVar(&config.UpstreamHost, "upstream", "agentn.global.api5.cursor.sh", "upstream api5 host[:port]")
	flag.StringVar(&config.UpstreamServerName, "upstream-servername", "", "upstream TLS SNI (default: upstream host)")
	flag.BoolVar(&config.UpstreamInsecure, "upstream-insecure", false, "skip upstream TLS verify")
	flag.StringVar(&config.CABundlePath, "ca-bundle", "", "PEM bundle with MITM CA cert+key; default: repo embedded CA")
	flag.StringVar(&config.CaptureDir, "capture-dir", "", "capture output directory (default: ./captures/<timestamp>)")
	flag.StringVar(&decodeDir, "decode", "", "decode an existing capture directory and exit")
	flag.Parse()

	if decodeDir != "" {
		if err := h2agentproxy.DecodeCaptureDirectory(decodeDir); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("decoded %s\n", decodeDir)
		return
	}

	if config.CABundlePath == "" {
		config.EmbeddedCACertPEM = certs.EmbeddedCACertPEM()
		config.EmbeddedCAKeyPEM = certs.EmbeddedCAKeyPEM()
	}

	server, err := h2agentproxy.NewServer(config)
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Start(); err != nil {
		log.Fatal(err)
	}

	caPath := server.CaptureDir() + "/ca.crt"
	fmt.Printf("H2 agent proxy listening on https://%s\n", server.ListenAddr())
	fmt.Printf("Upstream: https://%s\n", server.UpstreamHost())
	fmt.Printf("Capture dir: %s\n", server.CaptureDir())
	fmt.Printf("CA cert: %s\n", caPath)
	fmt.Printf("\nCursor CLI (capture official api5, not local mode):\n")
	fmt.Printf("  HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= \\\n")
	fmt.Printf("  NODE_EXTRA_CA_CERTS=%s \\\n", caPath)
	fmt.Printf("  agent --agent-endpoint https://127.0.0.1:%s -k --trust \\\n", listenPort(server.ListenAddr()))
	fmt.Printf("    -p \"Reply with exactly: OK\" --mode ask --output-format json\n")
	fmt.Printf("\nLocal mode (no official api5): cursor-local-assistant agent -- …\n")
	fmt.Printf("Note: --agent-endpoint and -k are hidden flags; they are supported on 2026.08.04+.\n")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "8443"
	}
	return port
}
