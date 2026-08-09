package proxydebugger

import (
	"fmt"
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

	// Default q is metadata-only: body hit must not match without SearchBody.
	items, total, _ = store.query(ExchangeQuery{Q: "AGENT_MODE_ASK", Include: "decoded", Limit: 10})
	if total != 0 {
		t.Fatalf("metadata-only q should miss body text, total=%d", total)
	}
	items, total, _ = store.query(ExchangeQuery{Q: "AGENT_MODE_ASK", SearchBody: true, Include: "decoded", Limit: 10})
	if total != 1 || items[0].ID != "1" {
		t.Fatalf("qBody search failed: total=%d items=%v", total, idsOf(items))
	}

	items, total, _ = store.query(ExchangeQuery{Q: "run_request", Include: "summary", Limit: 10})
	if total != 2 {
		t.Fatalf("metadata q on requestKind failed: total=%d", total)
	}
}

func TestAppendStreamingFrameBatchesPublishesAndAccountsBytes(t *testing.T) {
	store := newExchangeStore(defaultMaxStoreBytes, 0)
	store.create(&Exchange{ExchangeSummary: ExchangeSummary{ID: "r", Path: runSSEPath, StartedAt: time.Now()}})
	before := store.stats().UsedBytes
	for i := 0; i < framePublishInterval+1; i++ {
		store.appendStreamingFrame("r", true, FrameView{
			Index: i,
			Kind:  "interaction_update",
			JSON:  fmt.Sprintf(`{"i":%d,"pad":"%s"}`, i, strings.Repeat("x", 64)),
		}, defaultMaxFrames)
	}
	ex, ok := store.get("r")
	if !ok {
		t.Fatal("missing exchange")
	}
	if ex.FrameCount != framePublishInterval+1 {
		t.Fatalf("frameCount=%d", ex.FrameCount)
	}
	after := store.stats().UsedBytes
	if after <= before {
		t.Fatalf("usedBytes should grow: before=%d after=%d", before, after)
	}
	// Successful frames should not keep rawHex.
	for _, frame := range ex.Response.Frames {
		if frame.RawHex != "" {
			t.Fatal("successful streaming frames should drop rawHex")
		}
	}
}

func TestQueryLimitCapAndNewestFirst(t *testing.T) {
	store := newExchangeStore(defaultMaxStoreBytes, 0)
	for i := 0; i < 5; i++ {
		store.create(&Exchange{ExchangeSummary: ExchangeSummary{
			ID:        fmt.Sprintf("%d", i),
			Path:      "/x",
			StartedAt: time.Now().Add(time.Duration(i) * time.Second),
		}})
	}
	items, total, _ := store.query(ExchangeQuery{Limit: 2, Include: "summary"})
	if total != 5 || len(items) != 2 {
		t.Fatalf("total=%d len=%d", total, len(items))
	}
	if items[0].ID != "4" || items[1].ID != "3" {
		t.Fatalf("expected newest-first page, got %v", idsOf(items))
	}
	items, total, _ = store.query(ExchangeQuery{Limit: 10_000, Include: "summary"})
	if total != 5 || len(items) != 5 {
		t.Fatalf("limit cap failed: total=%d len=%d", total, len(items))
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
