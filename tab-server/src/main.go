// Command cursor-tab-server serves Cursor Tab completion over a
// Chat Completions upstream.
package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/leookun/cursor-byok/tab-server/src/config"
	"github.com/leookun/cursor-byok/tab-server/src/server"
)

const defaultConfigPath = "./config.yaml"
const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func main() {
	configPath := flag.String("config", defaultConfigPath, "配置文件路径")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	tabServer := server.New(cfg)
	httpServer := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           tabServer.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	log.Printf("cursor-tab-server 启动 listen=%s model=%s", cfg.Server.ListenAddr, cfg.Upstream.Model)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("监听失败: %v", err)
	}
	os.Exit(0)
}
