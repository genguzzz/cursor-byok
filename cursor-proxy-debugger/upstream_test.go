package proxydebugger

import "testing"

func TestDefaultProxyAddrAvoidsProxyman9090(t *testing.T) {
	cfg := Config{}.normalized()
	if cfg.ProxyAddr != "127.0.0.1:9092" {
		t.Fatalf("default proxy addr = %q, want 127.0.0.1:9092", cfg.ProxyAddr)
	}
	if cfg.UIAddr != "127.0.0.1:9091" {
		t.Fatalf("default ui addr = %q, want 127.0.0.1:9091", cfg.UIAddr)
	}
}

func TestUpstreamProxyTransportOptions(t *testing.T) {
	server, err := New(Config{
		ProxyAddr:     "127.0.0.1:19092",
		UIAddr:        "127.0.0.1:19091",
		UpstreamProxy: "http://127.0.0.1:18080",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proxyFunc, tlsConfig, err := server.upstreamTransportOptions()
	if err != nil {
		t.Fatalf("upstreamTransportOptions: %v", err)
	}
	if proxyFunc == nil {
		t.Fatal("expected proxy func")
	}
	if tlsConfig == nil || tlsConfig.RootCAs == nil {
		t.Fatal("expected TLS root CAs that include embedded CA")
	}

	server2, err := New(Config{
		ProxyAddr: "127.0.0.1:19092",
		UIAddr:    "127.0.0.1:19091",
	})
	if err != nil {
		t.Fatalf("New without upstream: %v", err)
	}
	proxyFunc, tlsConfig, err = server2.upstreamTransportOptions()
	if err != nil {
		t.Fatalf("upstreamTransportOptions empty: %v", err)
	}
	if proxyFunc != nil || tlsConfig != nil {
		t.Fatal("expected nil upstream options when UpstreamProxy empty")
	}
}
