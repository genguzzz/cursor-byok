package proxydebugger

import (
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cursor/internal/certs"
)

//go:embed web/*
var webAssets embed.FS

func (server *Server) newUIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", server.handleStatus)
	mux.HandleFunc("GET /api/exchanges", server.handleExchangeList)
	mux.HandleFunc("GET /api/exchanges/query", server.handleExchangeQuery)
	mux.HandleFunc("GET /api/exchanges/{id}", server.handleExchangeDetail)
	mux.HandleFunc("GET /api/exchanges/{id}/raw", server.handleExchangeRaw)
	mux.HandleFunc("DELETE /api/exchanges", server.handleClearExchanges)
	mux.HandleFunc("GET /api/events", server.handleEvents)
	mux.HandleFunc("GET /api/ca.crt", server.handleCACertificate)
	assets, _ := fs.Sub(webAssets, "web")
	fileServer := http.FileServer(http.FS(assets))
	mux.Handle("/", fileServer)
	return securityHeaders(mux)
}

func (server *Server) handleStatus(writer http.ResponseWriter, _ *http.Request) {
	stats := server.store.stats()
	writeJSON(writer, http.StatusOK, map[string]any{
		"proxyAddr":          server.config.ProxyAddr,
		"uiAddr":             server.config.UIAddr,
		"targetHost":         server.config.TargetHost,
		"targetHostPatterns": server.config.targetHostPatterns,
		"upstreamProxy":      server.config.UpstreamProxy,
		"running":            true,
		"store":              stats,
		"maxStoreBytes":      stats.MaxStoreBytes,
		"usedBytes":          stats.UsedBytes,
		"exchangeCount":      stats.Count,
	})
}

func (server *Server) handleExchangeList(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, server.store.summaries())
}

func (server *Server) handleExchangeQuery(writer http.ResponseWriter, request *http.Request) {
	query := parseExchangeQuery(request)
	items, total, stats := server.store.query(query)
	writeJSON(writer, http.StatusOK, map[string]any{
		"total":    total,
		"returned": len(items),
		"offset":   query.Offset,
		"limit":    query.Limit,
		"include":  firstNonEmpty(query.Include, "summary"),
		"store":    stats,
		"items":    items,
	})
}

func (server *Server) handleExchangeDetail(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("id"))
	exchange, ok := server.store.get(id)
	if !ok {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "请求记录不存在"})
		return
	}
	include := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("include")))
	if include == "" {
		include = "full"
	}
	writeJSON(writer, http.StatusOK, projectExchange(exchange, include))
}

func (server *Server) handleExchangeRaw(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("id"))
	exchange, ok := server.store.get(id)
	if !ok {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "请求记录不存在"})
		return
	}
	side := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("side")))
	if side == "" {
		side = "request"
	}
	format := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("format")))
	if format == "" {
		format = "hex"
	}
	var payload Payload
	switch side {
	case "request", "req":
		payload = exchange.Request
		side = "request"
	case "response", "resp":
		payload = exchange.Response
		side = "response"
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "side 仅支持 request|response"})
		return
	}
	if payload.RawHex == "" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "该侧没有 raw 抓包（可能已被淘汰或未捕获）"})
		return
	}
	switch format {
	case "json":
		writeJSON(writer, http.StatusOK, map[string]any{
			"id":           exchange.ID,
			"side":         side,
			"contentType":  payload.ContentType,
			"contentCodec": payload.ContentCodec,
			"size":         payload.Size,
			"rawTruncated": payload.RawTruncated,
			"rawHex":       payload.RawHex,
			"decodedJson":  payload.DecodedJSON,
			"decodeError":  payload.DecodeError,
			"frameCount":   len(payload.Frames),
		})
	case "hex":
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.hex"`, exchange.ID, side))
		_, _ = writer.Write([]byte(payload.RawHex))
	case "bin", "binary", "raw":
		raw, err := hex.DecodeString(payload.RawHex)
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "rawHex 解码失败: " + err.Error()})
			return
		}
		contentType := payload.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		writer.Header().Set("Content-Type", contentType)
		writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.bin"`, exchange.ID, side))
		_, _ = writer.Write(raw)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "format 仅支持 hex|bin|json"})
	}
}

func (server *Server) handleClearExchanges(writer http.ResponseWriter, _ *http.Request) {
	server.store.clear()
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "当前响应不支持流式刷新", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	updates, unsubscribe := server.store.subscribe()
	defer unsubscribe()
	fmt.Fprint(writer, "event: ready\ndata: {}\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-updates:
			if !open {
				return
			}
			payload, _ := json.Marshal(event)
			fmt.Fprintf(writer, "event: update\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(writer, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (server *Server) handleCACertificate(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/x-x509-ca-cert")
	writer.Header().Set("Content-Disposition", `attachment; filename="cursor-local-proxy-ca.crt"`)
	_, _ = writer.Write(certs.EmbeddedCACertPEM())
}

func parseExchangeQuery(request *http.Request) ExchangeQuery {
	values := request.URL.Query()
	query := ExchangeQuery{
		Server:        strings.TrimSpace(values.Get("server")),
		CaptureSource: strings.TrimSpace(values.Get("captureSource")),
		RequestKind:   strings.TrimSpace(firstNonEmpty(values.Get("requestKind"), values.Get("kind"))),
		ResponseKind:  strings.TrimSpace(values.Get("responseKind")),
		Method:        strings.TrimSpace(values.Get("method")),
		HostContains:  strings.TrimSpace(firstNonEmpty(values.Get("host"), values.Get("hostContains"))),
		PathContains:  strings.TrimSpace(firstNonEmpty(values.Get("path"), values.Get("pathContains"))),
		RequestID:     strings.TrimSpace(firstNonEmpty(values.Get("requestId"), values.Get("request_id"))),
		ID:            strings.TrimSpace(values.Get("id")),
		Q:             strings.TrimSpace(firstNonEmpty(values.Get("q"), values.Get("query"))),
		Include:       strings.TrimSpace(values.Get("include")),
		Limit:         atoiDefault(values.Get("limit"), 50),
		Offset:        atoiDefault(values.Get("offset"), 0),
		Status:        atoiDefault(values.Get("status"), 0),
		MinReqBytes:   int64(atoiDefault(values.Get("minRequestBytes"), 0)),
		MinRespBytes:  int64(atoiDefault(values.Get("minResponseBytes"), 0)),
	}
	if raw := strings.TrimSpace(values.Get("hasRaw")); raw != "" {
		value := strings.EqualFold(raw, "1") || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
		query.HasRaw = &value
	}
	if decoded := strings.TrimSpace(values.Get("hasDecoded")); decoded != "" {
		value := strings.EqualFold(decoded, "1") || strings.EqualFold(decoded, "true") || strings.EqualFold(decoded, "yes")
		query.HasDecoded = &value
	}
	if since := strings.TrimSpace(values.Get("since")); since != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, since); err == nil {
			query.Since = parsed
		} else if parsed, err := time.Parse(time.RFC3339, since); err == nil {
			query.Since = parsed
		}
	}
	if until := strings.TrimSpace(values.Get("until")); until != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, until); err == nil {
			query.Until = parsed
		} else if parsed, err := time.Parse(time.RFC3339, until); err == nil {
			query.Until = parsed
		}
	}
	return query
}

func atoiDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'")
		next.ServeHTTP(writer, request)
	})
}
