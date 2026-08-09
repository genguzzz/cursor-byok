package upstream

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/agent/protocol"
	"cursor/internal/backend/forwarder"
	"cursor/internal/backend/server"
	"cursor/internal/logger"
	legacyruntime "cursor/internal/runtime"

	"google.golang.org/protobuf/proto"
)

const defaultRunSSEClassifyWait = 2 * time.Second

type AgentBackend string

const (
	AgentBackendLocal    AgentBackend = "local"
	AgentBackendUpstream AgentBackend = "upstream"
)

type AgentRouter struct {
	table       *agentRouteTable
	localBidi   http.Handler
	localRunSSE http.Handler
	historyRoot string
	wait        time.Duration

	// deferLogOnce 避免同一 request_id 在 heartbeat/prewarm 上刷屏。
	deferLogOnce sync.Map // request_id -> struct{}
	// classifyFailLogMu 限制 classify 失败日志风暴（早期 gzip 误判曾达数百条/分钟）。
	classifyFailLogMu   sync.Mutex
	classifyFailLogLast time.Time
	classifyFailLogN    int
}

type agentRouteTable struct {
	mu      sync.Mutex
	entries map[string]AgentBackend
	waiters map[string][]chan AgentBackend
}

func NewAgentRouter(localBidi http.Handler, localRunSSE http.Handler, historyRoot string) *AgentRouter {
	return &AgentRouter{
		table:       newAgentRouteTable(),
		localBidi:   localBidi,
		localRunSSE: localRunSSE,
		historyRoot: historyRoot,
		wait:        defaultRunSSEClassifyWait,
	}
}

func newAgentRouteTable() *agentRouteTable {
	return &agentRouteTable{
		entries: make(map[string]AgentBackend),
		waiters: make(map[string][]chan AgentBackend),
	}
}

func (table *agentRouteTable) Lookup(requestID string) (AgentBackend, bool) {
	if table == nil {
		return "", false
	}
	table.mu.Lock()
	defer table.mu.Unlock()
	backend, ok := table.entries[strings.TrimSpace(requestID)]
	return backend, ok
}

func (table *agentRouteTable) Bind(requestID string, backend AgentBackend) {
	if table == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	table.mu.Lock()
	defer table.mu.Unlock()
	table.entries[requestID] = backend
	for _, waiter := range table.waiters[requestID] {
		select {
		case waiter <- backend:
		default:
		}
	}
	delete(table.waiters, requestID)
}

func (table *agentRouteTable) Wait(requestID string, timeout time.Duration) (AgentBackend, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || table == nil {
		return AgentBackendUpstream, false
	}
	table.mu.Lock()
	if backend, ok := table.entries[requestID]; ok {
		table.mu.Unlock()
		return backend, true
	}
	waiter := make(chan AgentBackend, 1)
	table.waiters[requestID] = append(table.waiters[requestID], waiter)
	table.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case backend := <-waiter:
		return backend, true
	case <-timer.C:
		table.mu.Lock()
		defer table.mu.Unlock()
		if backend, ok := table.entries[requestID]; ok {
			return backend, true
		}
		// 超时未分类：摘掉本 waiter，避免泄漏；由调用方 Bind(upstream)。
		current := table.waiters[requestID]
		remaining := make([]chan AgentBackend, 0, len(current))
		for _, candidate := range current {
			if candidate != waiter {
				remaining = append(remaining, candidate)
			}
		}
		if len(remaining) == 0 {
			delete(table.waiters, requestID)
		} else {
			table.waiters[requestID] = remaining
		}
		return AgentBackendUpstream, false
	}
}

func (router *AgentRouter) BidiAction(deps Dependencies) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, CompatRouteConfig{Name: "bidi_append"})
		if err != nil {
			return err
		}
		if reqCtx == nil {
			return fmt.Errorf("bidi request context is nil")
		}
		backend, err := router.classifyBidi(reqCtx)
		if err != nil {
			router.logClassifyFailed(err)
			backend = AgentBackendUpstream
		}
		return router.dispatch(reqCtx, backend, router.localBidi)
	}
}

func (router *AgentRouter) logClassifyFailed(err error) {
	if router == nil {
		return
	}
	now := time.Now()
	router.classifyFailLogMu.Lock()
	defer router.classifyFailLogMu.Unlock()
	router.classifyFailLogN++
	if router.classifyFailLogLast.IsZero() || now.Sub(router.classifyFailLogLast) >= 5*time.Second {
		n := router.classifyFailLogN
		router.classifyFailLogN = 0
		router.classifyFailLogLast = now
		if n > 1 {
			logger.Infof("mixed agent bidi classify failed, default upstream err=%v (x%d in window)", err, n)
		} else {
			logger.Infof("mixed agent bidi classify failed, default upstream err=%v", err)
		}
	}
}

func (router *AgentRouter) RunSSEAction(deps Dependencies) server.HandlerFunc {
	return func(ctx *server.Context) error {
		reqCtx, _, err := newCompatRouteObjects(ctx, deps, CompatRouteConfig{Name: "run_sse"})
		if err != nil {
			return err
		}
		if reqCtx == nil {
			return fmt.Errorf("run sse request context is nil")
		}
		requestID, err := peekRunSSERequestID(reqCtx)
		if err != nil || requestID == "" {
			logger.Infof("mixed agent runsse missing request_id err=%v", err)
			return router.dispatch(reqCtx, AgentBackendUpstream, router.localRunSSE)
		}
		backend, ok := router.table.Lookup(requestID)
		if !ok {
			wait := router.wait
			if wait <= 0 {
				wait = defaultRunSSEClassifyWait
			}
			backend, ok = router.table.Wait(requestID, wait)
			if !ok {
				backend = AgentBackendUpstream
				router.table.Bind(requestID, backend)
				logger.Infof("mixed agent runsse unclassified request_id=%s default=upstream", requestID)
			}
		}
		return router.dispatch(reqCtx, backend, router.localRunSSE)
	}
}

func (router *AgentRouter) classifyBidi(reqCtx *RequestContext) (AgentBackend, error) {
	appendReq := &aiserverv1.BidiAppendRequest{}
	body := reqCtx.RequestBody
	if reqCtx != nil {
		body = maybeHTTPDecompress(reqCtx.Headers, body)
	}
	if err := decodeProtoPayload(reqCtx.ContentType, body, appendReq); err != nil {
		return AgentBackendUpstream, err
	}
	requestID := protocol.NormalizeRequestID(protocol.ReadAppendRequestID(appendReq))
	if requestID == "" {
		return AgentBackendUpstream, fmt.Errorf("request_id is required")
	}
	message, _, err := protocol.DecodeAgentClientMessage(appendReq.GetData())
	if err != nil {
		return AgentBackendUpstream, err
	}
	if forwarder.ApplyEffectiveChildRunModel(message) {
		if err := rewriteBidiAppendAgentMessage(reqCtx, body, appendReq, message); err != nil {
			return AgentBackendUpstream, fmt.Errorf("rewrite child run model: %w", err)
		}
		logger.Infof("mixed agent child model rewritten request_id=%s model_id=%s", requestID, forwarder.ExtractRequestedModelID(message))
	}
	if backend, ok := router.table.Lookup(requestID); ok {
		return backend, nil
	}
	backend := router.classifyMessage(reqCtx, message)
	modelID := forwarder.ExtractRequestedModelID(message)
	// 无 model_id 的 heartbeat/prewarm 不锁亲和，留给后续 run_request 判定。
	if modelID != "" || backend == AgentBackendLocal {
		router.table.Bind(requestID, backend)
		logger.Infof("mixed agent route request_id=%s backend=%s model_id=%s", requestID, backend, modelID)
	} else if _, loaded := router.deferLogOnce.LoadOrStore(requestID, struct{}{}); !loaded {
		logger.Infof("mixed agent route defer bind request_id=%s (no model yet)", requestID)
	}
	return backend, nil
}

func rewriteBidiAppendAgentMessage(reqCtx *RequestContext, originalBody []byte, appendReq *aiserverv1.BidiAppendRequest, message *agentv1.AgentClientMessage) error {
	if reqCtx == nil || appendReq == nil || message == nil {
		return fmt.Errorf("rewrite inputs are required")
	}
	raw, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	appendReq.Data = hex.EncodeToString(raw)
	encoded, contentType, err := encodeRequestProtoPayload(reqCtx.ContentType, originalBody, appendReq)
	if err != nil {
		return err
	}
	reqCtx.RequestBody = encoded
	reqCtx.ContentType = contentType
	if reqCtx.Headers == nil {
		reqCtx.Headers = make(http.Header)
	}
	reqCtx.Headers.Set("Content-Type", contentType)
	reqCtx.Headers.Del("Content-Encoding")
	reqCtx.Headers.Set("Content-Length", strconv.Itoa(len(encoded)))
	if reqCtx.Request != nil {
		if reqCtx.Request.Header == nil {
			reqCtx.Request.Header = make(http.Header)
		}
		reqCtx.Request.Header.Set("Content-Type", contentType)
		reqCtx.Request.Header.Del("Content-Encoding")
		reqCtx.Request.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
		reqCtx.Request.ContentLength = int64(len(encoded))
	}
	return nil
}

func (router *AgentRouter) classifyMessage(reqCtx *RequestContext, message *agentv1.AgentClientMessage) AgentBackend {
	modelID := forwarder.ExtractEffectiveRunModelID(message)
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		adapters = nil
	}
	if matchesLocalAdapter(modelID, adapters) {
		return AgentBackendLocal
	}
	if strings.TrimSpace(modelID) != "" {
		return AgentBackendUpstream
	}
	conversationID := forwarder.ExtractConversationID(message)
	if forwarder.LocalHistoryExists(router.historyRoot, conversationID) {
		return AgentBackendLocal
	}
	return AgentBackendUpstream
}

func (router *AgentRouter) dispatch(reqCtx *RequestContext, backend AgentBackend, local http.Handler) error {
	if backend == AgentBackendLocal {
		return serveLocalHandler(reqCtx, local)
	}
	_, err := ForwardToUpstream(reqCtx, ForwardOptions{PreserveClientAuth: true})
	return err
}

func serveLocalHandler(reqCtx *RequestContext, handler http.Handler) error {
	if handler == nil || reqCtx == nil || reqCtx.Request == nil || reqCtx.ResponseWriter == nil {
		return fmt.Errorf("local agent handler is unavailable")
	}
	request := reqCtx.Request.Clone(reqCtx.Request.Context())
	request.Body = io.NopCloser(bytes.NewReader(reqCtx.RequestBody))
	request.ContentLength = int64(len(reqCtx.RequestBody))
	handler.ServeHTTP(reqCtx.ResponseWriter, request)
	return nil
}

func peekRunSSERequestID(reqCtx *RequestContext) (string, error) {
	if reqCtx == nil {
		return "", fmt.Errorf("nil request context")
	}
	message := &aiserverv1.BidiRequestId{}
	if err := decodeProtoPayload(reqCtx.ContentType, reqCtx.RequestBody, message); err != nil {
		return "", err
	}
	return protocol.NormalizeRequestID(protocol.ReadBidiRequestID(message)), nil
}

// matchesLocalAdapter 仅按渠道 hash（adapter.ID）判定，避免 provider modelID
// （如 composer-2.5）劫持官方同名模型。CLI 的 GetUsableModels.modelId 也是渠道 hash。
func matchesLocalAdapter(modelID string, adapters []legacyruntime.ModelAdapterConfig) bool {
	channels := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		if id := strings.TrimSpace(adapter.ID); id != "" {
			channels[id] = struct{}{}
		}
	}
	return isConfiguredChannelID(modelID, channels)
}

func isConfiguredChannelID(modelID string, channels map[string]struct{}) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || len(channels) == 0 {
		return false
	}
	if _, ok := channels[modelID]; ok {
		return true
	}
	if index := strings.LastIndex(modelID, ":"); index > 0 {
		if _, ok := channels[strings.TrimSpace(modelID[:index])]; ok {
			return true
		}
	}
	return false
}
