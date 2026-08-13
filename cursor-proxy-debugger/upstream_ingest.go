package proxydebugger

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// UpstreamHop 是 backend→官方 第二跳的抓包输入。
type UpstreamHop struct {
	StartedAt      time.Time
	Duration       time.Duration
	Method         string
	URL            string
	Host           string
	Path           string
	Status         int
	RequestID      string
	RequestHeader  http.Header
	ResponseHeader http.Header
	RequestBody    []byte
	ResponseBody   []byte
	Error          string
}

// IngestUpstreamHop 把第二跳写入与客户端 MITM 相同的 exchange 列表。
func (server *Server) IngestUpstreamHop(hop UpstreamHop) {
	if server == nil || server.store == nil {
		return
	}
	id := "u" + strconv.FormatUint(server.counter.Add(1), 10)
	method := strings.TrimSpace(hop.Method)
	if method == "" {
		method = http.MethodPost
	}
	host := hop.Host
	path := hop.Path
	if (host == "" || path == "") && hop.URL != "" {
		if parsed, err := http.NewRequest(method, hop.URL, nil); err == nil && parsed.URL != nil {
			if host == "" {
				host = parsed.URL.Host
			}
			if path == "" {
				path = parsed.URL.Path
			}
		}
	}
	startedAt := hop.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	state := "completed"
	if hop.Error != "" {
		state = "error"
	}
	reqCodec := requestContentCodec(path, hop.RequestHeader)
	respCodec := responseContentCodec(path, hop.ResponseHeader)
	exchange := &Exchange{
		ExchangeSummary: ExchangeSummary{
			ID:            id,
			StartedAt:     startedAt,
			Method:        method,
			URL:           hop.URL,
			Host:          host,
			Path:          path,
			Status:        hop.Status,
			State:         state,
			DurationMS:    hop.Duration.Milliseconds(),
			RequestID:     hop.RequestID,
			CaptureSource: CaptureSourceUpstream,
			Server:        ServerOfficial,
			Error:         hop.Error,
		},
		Request: Payload{
			Headers:      sortedHeaders(hop.RequestHeader),
			ContentType:  firstHeaderMap(hop.RequestHeader, "Content-Type"),
			ContentCodec: reqCodec,
			Frames:       make([]FrameView, 0),
		},
		Response: Payload{
			Headers:      sortedHeaders(hop.ResponseHeader),
			ContentType:  firstHeaderMap(hop.ResponseHeader, "Content-Type"),
			ContentCodec: respCodec,
			Frames:       make([]FrameView, 0),
		},
	}
	if exchange.RequestID == "" {
		exchange.RequestID = firstHeaderMap(hop.RequestHeader, "x-request-id")
	}
	server.store.create(exchange)

	truncatedReq := int64(len(hop.RequestBody)) >= int64(server.config.MaxCaptureBytes)
	server.finishRequestBody(id, path, reqCodec, hop.RequestBody, int64(len(hop.RequestBody)), truncatedReq, nil)
	truncatedResp := int64(len(hop.ResponseBody)) >= int64(server.config.MaxCaptureBytes)
	server.finishResponseBody(id, path, respCodec, hop.ResponseBody, int64(len(hop.ResponseBody)), truncatedResp, nil)
	if hop.Error != "" {
		server.store.update(id, func(exchange *Exchange) {
			exchange.State = "error"
			exchange.Error = hop.Error
		})
	}
}

// ProviderHop 是 localserver→模型 provider 的第三跳抓包输入。
type ProviderHop struct {
	StartedAt      time.Time
	Duration       time.Duration
	Method         string
	URL            string
	Host           string
	Path           string
	Status         int
	RequestID      string
	ModelCallID    string
	Provider       string
	RequestHeader  map[string][]string
	ResponseHeader map[string][]string
	RequestBody    []byte
	ResponseBody   []byte
	Error          string
}

// IngestProviderHop 把 localserver 出站 provider 流量写入统一 exchange 列表。
func (server *Server) IngestProviderHop(hop ProviderHop) {
	if server == nil || server.store == nil {
		return
	}
	id := "p" + strconv.FormatUint(server.counter.Add(1), 10)
	method := firstNonEmpty(hop.Method, http.MethodPost)
	host, path := hop.Host, hop.Path
	if (host == "" || path == "") && hop.URL != "" {
		if parsed, err := http.NewRequest(method, hop.URL, nil); err == nil && parsed.URL != nil {
			host = firstNonEmpty(host, parsed.URL.Host)
			path = firstNonEmpty(path, parsed.URL.Path)
		}
	}
	startedAt := hop.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	state := "completed"
	if hop.Error != "" {
		state = "error"
	}
	exchange := &Exchange{ExchangeSummary: ExchangeSummary{
		ID: id, StartedAt: startedAt, Method: method, URL: hop.URL, Host: host, Path: path,
		Status: hop.Status, State: state, DurationMS: hop.Duration.Milliseconds(), RequestID: hop.RequestID,
		ModelCallID: hop.ModelCallID,
		RequestKind: "provider_request", ResponseKind: "provider_stream", CaptureSource: CaptureSourceProvider,
		Server: ServerProvider, Error: hop.Error,
	}, Request: Payload{Headers: headersFromMap(hop.RequestHeader), ContentType: firstHeaderMap(hop.RequestHeader, "Content-Type"), Frames: make([]FrameView, 0)}, Response: Payload{Headers: headersFromMap(hop.ResponseHeader), ContentType: firstHeaderMap(hop.ResponseHeader, "Content-Type"), Frames: make([]FrameView, 0)}}
	if exchange.RequestID == "" {
		exchange.RequestID = firstHeaderMap(hop.RequestHeader, "x-request-id")
	}
	server.store.create(exchange)
	server.finishProviderRequest(id, hop.RequestBody)
	server.finishProviderResponse(id, hop.ResponseBody)
}

func (server *Server) finishProviderRequest(id string, body []byte) {
	server.store.update(id, func(exchange *Exchange) {
		exchange.RequestBytes = int64(len(body))
		exchange.Request.Size = int64(len(body))
		exchange.Request.RawHex = rawHex(body)
		if text, _, ok := fallbackDisplayBody(exchange.Request.ContentType, body); ok {
			exchange.Request.DecodedJSON = text
			exchange.Request.Frames = []FrameView{syntheticUnaryFrame(exchange.Path, "provider_request", exchange.RequestID, text, len(body))}
		}
	})
}

func (server *Server) finishProviderResponse(id string, body []byte) {
	server.store.update(id, func(exchange *Exchange) {
		exchange.ResponseBytes = int64(len(body))
		exchange.Response.Size = int64(len(body))
		exchange.Response.RawHex = rawHex(body)
		if text, _, ok := fallbackDisplayBody(exchange.Response.ContentType, body); ok {
			exchange.Response.DecodedJSON = text
			exchange.Response.Frames = []FrameView{syntheticUnaryFrame(exchange.Path, "provider_stream", "", text, len(body))}
		}
	})
}

func headersFromMap(values map[string][]string) []Header {
	if values == nil {
		return nil
	}
	return sortedHeaders(values)
}

func firstHeaderMap(values map[string][]string, name string) string {
	for key, items := range values {
		if strings.EqualFold(key, name) && len(items) > 0 {
			return strings.TrimSpace(items[0])
		}
	}
	return ""
}
