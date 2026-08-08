package proxydebugger

import "testing"

func TestHostMatchesPatternsWildcard(t *testing.T) {
	patterns := parseTargetHostPatterns("*.cursor.sh")
	for _, host := range []string{"api2.cursor.sh", "api3.cursor.sh", "api5.cursor.sh:443", "agentn.global.api5.cursor.sh"} {
		if !hostMatchesPatterns(host, patterns) {
			t.Fatalf("expected %q to match *.cursor.sh", host)
		}
	}
	if hostMatchesPatterns("example.com", patterns) {
		t.Fatal("example.com must not match")
	}
}

func TestHostMatchesPatternsSingle(t *testing.T) {
	patterns := parseTargetHostPatterns("api2.cursor.sh")
	if !hostMatchesPatterns("api2.cursor.sh:443", patterns) {
		t.Fatal("api2 with port should match")
	}
	if hostMatchesPatterns("api3.cursor.sh", patterns) {
		t.Fatal("api3 should not match single api2 pattern")
	}
}

func TestDefaultTargetHostIsWildcard(t *testing.T) {
	cfg := Config{}.normalized()
	if cfg.TargetHost != "*.cursor.sh" {
		t.Fatalf("TargetHost=%q", cfg.TargetHost)
	}
	if !hostMatchesPatterns("api2.cursor.sh", cfg.targetHostPatterns) {
		t.Fatal("default patterns should match api2")
	}
}

func TestClientHopServerDependsOnUpstreamProxy(t *testing.T) {
	local, err := New(Config{
		ProxyAddr:     "127.0.0.1:19093",
		UIAddr:        "127.0.0.1:19094",
		UpstreamProxy: "http://127.0.0.1:18080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := local.clientHopServer(); got != ServerLocal {
		t.Fatalf("with upstream proxy: got %q, want local", got)
	}

	direct, err := New(Config{
		ProxyAddr: "127.0.0.1:19095",
		UIAddr:    "127.0.0.1:19096",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := direct.clientHopServer(); got != ServerOfficial {
		t.Fatalf("without upstream proxy: got %q, want official", got)
	}
}
