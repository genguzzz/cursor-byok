package proxydebugger

import (
	"strings"
	"testing"
	"time"
)

func TestStoreEvictsOldestByByteBudget(t *testing.T) {
	store := newExchangeStore(2_000, 0)
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		store.create(&Exchange{
			ExchangeSummary: ExchangeSummary{
				ID:        id,
				StartedAt: time.Now().Add(time.Duration(i) * time.Second),
				Path:      "/aiserver.v1.BidiService/BidiAppend",
				Server:    ServerOfficial,
			},
			Request: Payload{
				RawHex:      strings.Repeat("ab", 400), // 800 hex chars
				DecodedJSON: `{"agent_client_kind":"run_request"}`,
			},
		})
	}
	stats := store.stats()
	if stats.UsedBytes > stats.MaxStoreBytes {
		t.Fatalf("usedBytes=%d exceeds max=%d", stats.UsedBytes, stats.MaxStoreBytes)
	}
	if stats.Count == 0 || stats.Count >= 5 {
		t.Fatalf("expected eviction to keep a subset, count=%d", stats.Count)
	}
	// newest should survive
	if _, ok := store.get("e"); !ok {
		t.Fatal("newest exchange e should remain")
	}
	if _, ok := store.get("a"); ok {
		t.Fatal("oldest exchange a should have been evicted")
	}
}

func TestStoreQueryFiltersServerAndKind(t *testing.T) {
	store := newExchangeStore(defaultMaxStoreBytes, 0)
	store.create(&Exchange{ExchangeSummary: ExchangeSummary{
		ID: "1", Server: ServerOfficial, RequestKind: "run_request", Path: "/aiserver.v1.BidiService/BidiAppend",
		StartedAt: time.Now(),
	}, Request: Payload{DecodedJSON: `{"mode":"AGENT_MODE_ASK"}`}})
	store.create(&Exchange{ExchangeSummary: ExchangeSummary{
		ID: "2", Server: ServerLocal, RequestKind: "kv_client_message", Path: "/aiserver.v1.BidiService/BidiAppend",
		StartedAt: time.Now(),
	}})
	store.create(&Exchange{ExchangeSummary: ExchangeSummary{
		ID: "3", Server: ServerOfficial, RequestKind: "run_request", Path: "/agent.v1.AgentService/RunSSE",
		StartedAt: time.Now(), ResponseBytes: 1000,
	}, Response: Payload{RawHex: "00aa"}})

	items, total, _ := store.query(ExchangeQuery{
		Server:      ServerOfficial,
		RequestKind: "run_request",
		Include:     "summary",
		Limit:       10,
	})
	if total != 2 {
		t.Fatalf("total=%d want 2", total)
	}
	if len(items) != 2 {
		t.Fatalf("returned=%d want 2", len(items))
	}

	items, total, _ = store.query(ExchangeQuery{
		PathContains: "RunSSE",
		HasRaw:       boolPtr(true),
		Include:      "raw",
		Limit:        10,
	})
	if total != 1 || len(items) != 1 || items[0].ID != "3" {
		t.Fatalf("RunSSE raw query failed: total=%d items=%v", total, idsOf(items))
	}
	if items[0].Response.RawHex == "" {
		t.Fatal("include=raw should keep rawHex")
	}
	if items[0].Request.DecodedJSON != "" {
		t.Fatal("include=raw should drop decodedJson")
	}

	items, total, _ = store.query(ExchangeQuery{Q: "AGENT_MODE_ASK", Include: "decoded", Limit: 10})
	if total != 1 || items[0].ID != "1" {
		t.Fatalf("q search failed: total=%d items=%v", total, idsOf(items))
	}
}

func idsOf(items []Exchange) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func boolPtr(value bool) *bool { return &value }
