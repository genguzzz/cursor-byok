package proxydebugger

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type exchangeStore struct {
	mu             sync.RWMutex
	maxStoreBytes  int64
	maxExchanges   int
	order          []string // newest first
	exchanges      map[string]*Exchange
	usedBytes      int64
	subscribers    map[chan storeEvent]struct{}
}

func newExchangeStore(maxStoreBytes int64, maxExchanges int) *exchangeStore {
	if maxStoreBytes <= 0 {
		maxStoreBytes = defaultMaxStoreBytes
	}
	return &exchangeStore{
		maxStoreBytes: maxStoreBytes,
		maxExchanges:  maxExchanges,
		exchanges:     make(map[string]*Exchange),
		subscribers:   make(map[chan storeEvent]struct{}),
	}
}

func (store *exchangeStore) create(exchange *Exchange) {
	store.mu.Lock()
	exchange.StoredBytes = estimateExchangeBytes(*exchange)
	store.exchanges[exchange.ID] = exchange
	store.order = append([]string{exchange.ID}, store.order...)
	store.usedBytes += exchange.StoredBytes
	evicted := store.evictLocked()
	store.mu.Unlock()
	store.publish(storeEvent{Type: "created", ID: exchange.ID})
	for _, id := range evicted {
		store.publish(storeEvent{Type: "evicted", ID: id})
	}
}

func (store *exchangeStore) update(id string, apply func(*Exchange)) {
	store.mu.Lock()
	exchange := store.exchanges[id]
	if exchange == nil {
		store.mu.Unlock()
		return
	}
	oldBytes := exchange.StoredBytes
	apply(exchange)
	exchange.StoredBytes = estimateExchangeBytes(*exchange)
	store.usedBytes += exchange.StoredBytes - oldBytes
	if store.usedBytes < 0 {
		store.usedBytes = 0
	}
	evicted := store.evictLocked()
	store.mu.Unlock()
	store.publish(storeEvent{Type: "updated", ID: id})
	for _, eid := range evicted {
		store.publish(storeEvent{Type: "evicted", ID: eid})
	}
}

// evictLocked drops oldest exchanges until under byte/count budgets.
// Caller must hold store.mu.
func (store *exchangeStore) evictLocked() []string {
	var evicted []string
	for len(store.order) > 0 {
		overBytes := store.usedBytes > store.maxStoreBytes
		overCount := store.maxExchanges > 0 && len(store.order) > store.maxExchanges
		if !overBytes && !overCount {
			break
		}
		oldest := store.order[len(store.order)-1]
		store.order = store.order[:len(store.order)-1]
		if exchange := store.exchanges[oldest]; exchange != nil {
			store.usedBytes -= exchange.StoredBytes
			if store.usedBytes < 0 {
				store.usedBytes = 0
			}
			delete(store.exchanges, oldest)
			evicted = append(evicted, oldest)
		} else {
			delete(store.exchanges, oldest)
		}
	}
	return evicted
}

func (store *exchangeStore) summaries() []ExchangeSummary {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]ExchangeSummary, 0, len(store.order))
	for _, id := range store.order {
		if exchange := store.exchanges[id]; exchange != nil {
			result = append(result, exchange.ExchangeSummary)
		}
	}
	return result
}

func (store *exchangeStore) stats() StoreStats {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return StoreStats{
		Count:         len(store.order),
		UsedBytes:     store.usedBytes,
		MaxStoreBytes: store.maxStoreBytes,
		MaxExchanges:  store.maxExchanges,
	}
}

func (store *exchangeStore) get(id string) (Exchange, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	exchange := store.exchanges[id]
	if exchange == nil {
		return Exchange{}, false
	}
	return cloneExchange(*exchange), true
}

func (store *exchangeStore) query(q ExchangeQuery) (items []Exchange, total int, stats StoreStats) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	stats = StoreStats{
		Count:         len(store.order),
		UsedBytes:     store.usedBytes,
		MaxStoreBytes: store.maxStoreBytes,
		MaxExchanges:  store.maxExchanges,
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	matched := make([]*Exchange, 0, 64)
	for _, id := range store.order {
		exchange := store.exchanges[id]
		if exchange == nil || !exchangeMatches(*exchange, q) {
			continue
		}
		matched = append(matched, exchange)
	}
	total = len(matched)
	if q.Offset >= total {
		return nil, total, stats
	}
	end := q.Offset + q.Limit
	if end > total {
		end = total
	}
	items = make([]Exchange, 0, end-q.Offset)
	for _, exchange := range matched[q.Offset:end] {
		items = append(items, projectExchange(*exchange, q.Include))
	}
	return items, total, stats
}

func exchangeMatches(exchange Exchange, q ExchangeQuery) bool {
	if q.ID != "" && !strings.EqualFold(exchange.ID, q.ID) {
		return false
	}
	if q.Server != "" && !strings.EqualFold(exchange.Server, q.Server) {
		return false
	}
	if q.CaptureSource != "" && !strings.EqualFold(exchange.CaptureSource, q.CaptureSource) {
		return false
	}
	if q.RequestKind != "" && !strings.EqualFold(exchange.RequestKind, q.RequestKind) {
		return false
	}
	if q.ResponseKind != "" && !strings.EqualFold(exchange.ResponseKind, q.ResponseKind) {
		return false
	}
	if q.Method != "" && !strings.EqualFold(exchange.Method, q.Method) {
		return false
	}
	if q.HostContains != "" && !strings.Contains(strings.ToLower(exchange.Host), strings.ToLower(q.HostContains)) {
		return false
	}
	if q.PathContains != "" && !strings.Contains(strings.ToLower(exchange.Path), strings.ToLower(q.PathContains)) {
		return false
	}
	if q.RequestID != "" && !strings.Contains(strings.ToLower(exchange.RequestID), strings.ToLower(q.RequestID)) {
		return false
	}
	if q.Status != 0 && exchange.Status != q.Status {
		return false
	}
	if q.MinReqBytes > 0 && exchange.RequestBytes < q.MinReqBytes {
		return false
	}
	if q.MinRespBytes > 0 && exchange.ResponseBytes < q.MinRespBytes {
		return false
	}
	if !q.Since.IsZero() && exchange.StartedAt.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && exchange.StartedAt.After(q.Until) {
		return false
	}
	if q.HasRaw != nil {
		has := exchange.Request.RawHex != "" || exchange.Response.RawHex != ""
		if *q.HasRaw != has {
			return false
		}
	}
	if q.HasDecoded != nil {
		has := exchange.Request.DecodedJSON != "" || exchange.Response.DecodedJSON != "" || len(exchange.Response.Frames) > 0
		if *q.HasDecoded != has {
			return false
		}
	}
	if q.Q != "" {
		needle := strings.ToLower(q.Q)
		haystack := strings.ToLower(strings.Join([]string{
			exchange.ID,
			exchange.Path,
			exchange.URL,
			exchange.Host,
			exchange.RequestID,
			exchange.RequestKind,
			exchange.ResponseKind,
			exchange.Server,
			exchange.CaptureSource,
			exchange.Error,
			exchange.Request.DecodedJSON,
			exchange.Response.DecodedJSON,
		}, "\n"))
		if !strings.Contains(haystack, needle) {
			// also search a few frame json snippets
			found := false
			for _, frame := range exchange.Response.Frames {
				if strings.Contains(strings.ToLower(frame.JSON), needle) || strings.Contains(strings.ToLower(frame.Kind), needle) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func projectExchange(exchange Exchange, include string) Exchange {
	include = strings.ToLower(strings.TrimSpace(include))
	if include == "" || include == "summary" {
		return Exchange{ExchangeSummary: exchange.ExchangeSummary}
	}
	cloned := cloneExchange(exchange)
	switch include {
	case "full", "all":
		return cloned
	case "decoded":
		cloned.Request.RawHex = ""
		cloned.Response.RawHex = ""
		for i := range cloned.Request.Frames {
			cloned.Request.Frames[i].RawHex = ""
		}
		for i := range cloned.Response.Frames {
			cloned.Response.Frames[i].RawHex = ""
		}
		return cloned
	case "raw":
		cloned.Request.DecodedJSON = ""
		cloned.Response.DecodedJSON = ""
		cloned.Request.Frames = nil
		cloned.Response.Frames = nil
		return cloned
	case "frames":
		cloned.Request.RawHex = ""
		cloned.Response.RawHex = ""
		cloned.Request.DecodedJSON = ""
		cloned.Response.DecodedJSON = ""
		return cloned
	default:
		return Exchange{ExchangeSummary: exchange.ExchangeSummary}
	}
}

func (store *exchangeStore) clear() {
	store.mu.Lock()
	store.order = nil
	store.exchanges = make(map[string]*Exchange)
	store.usedBytes = 0
	store.mu.Unlock()
	store.publish(storeEvent{Type: "cleared"})
}

func (store *exchangeStore) subscribe() (<-chan storeEvent, func()) {
	updates := make(chan storeEvent, 32)
	store.mu.Lock()
	store.subscribers[updates] = struct{}{}
	store.mu.Unlock()
	return updates, func() {
		store.mu.Lock()
		if _, ok := store.subscribers[updates]; ok {
			delete(store.subscribers, updates)
			close(updates)
		}
		store.mu.Unlock()
	}
}

func (store *exchangeStore) publish(event storeEvent) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for subscriber := range store.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func estimateExchangeBytes(exchange Exchange) int64 {
	n := int64(128)
	n += int64(len(exchange.ID) + len(exchange.URL) + len(exchange.Host) + len(exchange.Path) + len(exchange.RequestID))
	n += int64(len(exchange.RequestKind) + len(exchange.ResponseKind) + len(exchange.Error))
	n += estimatePayloadBytes(exchange.Request)
	n += estimatePayloadBytes(exchange.Response)
	return n
}

func estimatePayloadBytes(payload Payload) int64 {
	n := int64(64 + len(payload.ContentType) + len(payload.ContentCodec) + len(payload.DecodedJSON) + len(payload.DecodeError) + len(payload.RawHex))
	for _, header := range payload.Headers {
		n += int64(len(header.Name) + len(header.Value) + 8)
	}
	for _, frame := range payload.Frames {
		n += int64(64 + len(frame.Kind) + len(frame.MessageType) + len(frame.RequestID) + len(frame.JSON) + len(frame.RawHex) + len(frame.Error))
	}
	return n
}

func cloneExchange(exchange Exchange) Exchange {
	exchange.Request = clonePayload(exchange.Request)
	exchange.Response = clonePayload(exchange.Response)
	return exchange
}

func clonePayload(payload Payload) Payload {
	payload.Headers = append([]Header(nil), payload.Headers...)
	payload.Frames = append([]FrameView(nil), payload.Frames...)
	return payload
}

func elapsedMS(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return time.Since(startedAt).Milliseconds()
}

func sortedHeaders(headers map[string][]string) []Header {
	result := make([]Header, 0, len(headers))
	for name, values := range headers {
		value := ""
		for index, item := range values {
			if index > 0 {
				value += ", "
			}
			value += item
		}
		if isSensitiveHeader(name) && value != "" {
			value = "[已隐藏]"
		}
		result = append(result, Header{Name: name, Value: value})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	return result
}

func isSensitiveHeader(name string) bool {
	switch httpCanonicalLower(name) {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key":
		return true
	default:
		return false
	}
}

func httpCanonicalLower(value string) string {
	buffer := make([]byte, len(value))
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		buffer[index] = character
	}
	return string(buffer)
}
