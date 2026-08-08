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
			Error:         hop.Error,
		},
		Request: Payload{
			Headers:      sortedHeaders(hop.RequestHeader),
			ContentType:  firstHeader(hop.RequestHeader, "Content-Type"),
			ContentCodec: reqCodec,
			Frames:       make([]FrameView, 0),
		},
		Response: Payload{
			Headers:      sortedHeaders(hop.ResponseHeader),
			ContentType:  firstHeader(hop.ResponseHeader, "Content-Type"),
			ContentCodec: respCodec,
			Frames:       make([]FrameView, 0),
		},
	}
	if exchange.RequestID == "" {
		exchange.RequestID = firstHeader(hop.RequestHeader, "x-request-id")
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

func firstHeader(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(name))
}
