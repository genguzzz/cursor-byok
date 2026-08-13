package proxydebugger

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cursor/internal/certs"

	"github.com/elazarl/goproxy"
)

type exchangeContext struct {
	id string
}

// trackingListener wraps a net.Listener and records every accepted connection
// so Close can force-close hijacked (CONNECT/MITM) and long-lived (SSE)
// connections that http.Server.Shutdown neither tracks nor closes.
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newTrackingListener(listener net.Listener) *trackingListener {
	return &trackingListener{Listener: listener, conns: make(map[net.Conn]struct{})}
}

func (listener *trackingListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, listener: listener}
	listener.mu.Lock()
	listener.conns[tracked] = struct{}{}
	listener.mu.Unlock()
	return tracked, nil
}

func (listener *trackingListener) closeActive() {
	listener.mu.Lock()
	conns := make([]net.Conn, 0, len(listener.conns))
	for conn := range listener.conns {
		conns = append(conns, conn)
	}
	listener.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type trackedConn struct {
	net.Conn
	listener *trackingListener
}

func (conn *trackedConn) Close() error {
	err := conn.Conn.Close()
	conn.listener.mu.Lock()
	delete(conn.listener.conns, conn)
	conn.listener.mu.Unlock()
	return err
}

// Server runs the HTTPS debugging proxy and its local web UI.
type Server struct {
	config      Config
	certManager *certs.Manager
	store       *exchangeStore
	counter     atomic.Uint64
	proxyServer *http.Server
	uiServer    *http.Server
	proxyLn     *trackingListener
	uiLn        *trackingListener
	upstreamTr  *http.Transport
	runMu       sync.Mutex
}

// New creates a standalone Cursor protocol debugger.
func New(config Config) (*Server, error) {
	config = config.normalized()
	if err := validateLoopbackAddress(config.UIAddr); err != nil {
		return nil, err
	}
	manager, err := certs.NewEmbeddedManager()
	if err != nil {
		return nil, fmt.Errorf("加载 MITM CA 失败：%w", err)
	}
	server := &Server{
		config:      config,
		certManager: manager,
		store:       newExchangeStore(config.MaxStoreBytes, config.MaxExchanges),
	}
	proxyHandler, err := server.newProxyHandler()
	if err != nil {
		return nil, err
	}
	server.proxyServer = &http.Server{
		Handler:  proxyHandler,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	server.uiServer = &http.Server{Handler: server.newUIHandler()}
	return server, nil
}

// Start starts both listeners without modifying Cursor or system proxy settings.
func (server *Server) Start() error {
	server.runMu.Lock()
	defer server.runMu.Unlock()
	if server.proxyLn != nil || server.uiLn != nil {
		return errors.New("调试代理已经启动")
	}
	proxyListener, err := net.Listen("tcp", server.config.ProxyAddr)
	if err != nil {
		return fmt.Errorf("启动代理监听失败：%w", err)
	}
	uiListener, err := net.Listen("tcp", server.config.UIAddr)
	if err != nil {
		_ = proxyListener.Close()
		return fmt.Errorf("启动调试界面失败：%w", err)
	}
	server.proxyLn = newTrackingListener(proxyListener)
	server.uiLn = newTrackingListener(uiListener)
	go func() { _ = server.proxyServer.Serve(server.proxyLn) }()
	go func() { _ = server.uiServer.Serve(server.uiLn) }()
	return nil
}

// Close stops both listeners.
func (server *Server) Close(ctx context.Context) error {
	server.runMu.Lock()
	proxyServer := server.proxyServer
	uiServer := server.uiServer
	proxyLn := server.proxyLn
	uiLn := server.uiLn
	server.proxyLn = nil
	server.uiLn = nil
	server.runMu.Unlock()
	var errorsList []error
	// Force-close active (hijacked MITM tunnels + long-lived SSE streams) BEFORE
	// Shutdown. Shutdown only waits for non-hijacked connections to go idle and
	// never cancels their context, so the UI's never-idle /api/events stream
	// would otherwise make Shutdown block for the full context deadline before
	// these connections were reclaimed (the menubar passes a 3s context).
	if proxyLn != nil {
		proxyLn.closeActive()
	}
	if uiLn != nil {
		uiLn.closeActive()
	}
	if proxyServer != nil {
		if err := proxyServer.Shutdown(ctx); err != nil {
			errorsList = append(errorsList, err)
		}
	}
	if uiServer != nil {
		if err := uiServer.Shutdown(ctx); err != nil {
			errorsList = append(errorsList, err)
		}
	}
	// Release pooled upstream connections and captured exchanges immediately
	// instead of waiting for GC/idle-timeout, so a stop/start cycle (e.g. the
	// menubar's debug toggle) actually frees the bodies/frames/rawHex memory
	// budget rather than letting it linger for IdleConnTimeout.
	if server.upstreamTr != nil {
		server.upstreamTr.CloseIdleConnections()
	}
	if server.store != nil {
		server.store.clear()
	}
	// The capture store can hold up to MaxStoreBytes (default 200 MiB) of
	// bodies/frames/rawHex. Go keeps that heap reserved for reuse after GC, so
	// return it to the OS now that debug is being torn down.
	debug.FreeOSMemory()
	return errors.Join(errorsList...)
}

func (server *Server) ProxyAddr() string { return server.config.ProxyAddr }
func (server *Server) UIAddr() string    { return server.config.UIAddr }
func (server *Server) UIURL() string     { return "http://" + browserAddress(server.config.UIAddr) }

func (server *Server) newProxyHandler() (*goproxy.ProxyHttpServer, error) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.AllowHTTP2 = true
	proxy.Logger = log.New(io.Discard, "", 0)
	proxy.ConnectionErrHandler = func(_ io.Writer, context *goproxy.ProxyCtx, connectionErr error) {
		id := exchangeID(context)
		if id == "" {
			return
		}
		server.store.update(id, func(exchange *Exchange) {
			exchange.State = "error"
			exchange.Error = connectionErr.Error()
			exchange.DurationMS = elapsedMS(exchange.StartedAt)
		})
	}
	proxyFunc, tlsConfig, err := server.upstreamTransportOptions()
	if err != nil {
		return nil, err
	}
	upstreamTransport := &http.Transport{
		Proxy:                 proxyFunc,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
	}
	proxy.Tr = upstreamTransport
	server.upstreamTr = upstreamTransport

	caCertificate, err := server.certManager.CATLSCertificate()
	if err != nil {
		return nil, fmt.Errorf("读取 MITM CA 失败：%w", err)
	}
	baseTLSConfig := goproxy.TLSConfigFromCA(caCertificate)
	mitmAction := &goproxy.ConnectAction{
		Action: goproxy.ConnectMitm,
		TLSConfig: func(host string, context *goproxy.ProxyCtx) (*tls.Config, error) {
			return baseTLSConfig(host, context)
		},
	}
	proxy.OnRequest().HandleConnectFunc(func(host string, _ *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if server.matchesTargetHost(host) {
			return mitmAction, host
		}
		return goproxy.OkConnect, host
	})

	proxy.OnRequest().DoFunc(server.captureRequest)
	proxy.OnResponse().DoFunc(server.captureResponse)
	return proxy, nil
}

func (server *Server) captureRequest(request *http.Request, context *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if request == nil || request.Method == http.MethodConnect || !server.matchesHTTPRequest(request) {
		return request, nil
	}
	id := strconv.FormatUint(server.counter.Add(1), 10)
	path := request.URL.Path
	requestCodec := requestContentCodec(path, request.Header)
	exchange := &Exchange{
		ExchangeSummary: ExchangeSummary{
			ID:            id,
			StartedAt:     time.Now(),
			Method:        request.Method,
			URL:           request.URL.String(),
			Host:          request.URL.Host,
			Path:          path,
			State:         "pending",
			CaptureSource: CaptureSourceClient,
			Server:        server.clientHopServer(),
		},
		Request: Payload{
			Headers:      sortedHeaders(request.Header),
			ContentType:  request.Header.Get("Content-Type"),
			ContentCodec: requestCodec,
			Frames:       make([]FrameView, 0),
		},
		Response: Payload{Headers: make([]Header, 0), Frames: make([]FrameView, 0)},
	}
	server.store.create(exchange)
	context.UserData = exchangeContext{id: id}

	if request.Body == nil {
		server.finishRequestBody(id, path, requestCodec, nil, 0, false, nil)
		return request, nil
	}
	var frameDecoder *connectFrameDecoder
	if path == runSSEPath {
		frameDecoder = newConnectFrameDecoder(
			runSSERequestMessageType,
			requestCodec,
			server.config.MaxFrames,
			func(frame FrameView) { server.appendRequestFrame(id, frame) },
		)
	}
	request.Body = newCaptureReadCloser(
		request.Body,
		server.config.MaxCaptureBytes,
		func(chunk []byte) {
			if frameDecoder != nil {
				frameDecoder.Write(chunk)
			}
		},
		func(captured []byte, size int64, truncated bool, readErr error) {
			if frameDecoder != nil {
				frameDecoder.Close()
			}
			server.finishRequestBody(id, path, requestCodec, captured, size, truncated, readErr)
		},
	)
	return request, nil
}

func (server *Server) captureResponse(response *http.Response, context *goproxy.ProxyCtx) *http.Response {
	id := exchangeID(context)
	if id == "" || response == nil {
		return response
	}
	path := ""
	if response.Request != nil && response.Request.URL != nil {
		path = response.Request.URL.Path
	}
	responseCodec := responseContentCodec(path, response.Header)
	server.store.update(id, func(exchange *Exchange) {
		exchange.Status = response.StatusCode
		exchange.State = "streaming"
		exchange.DurationMS = elapsedMS(exchange.StartedAt)
		exchange.Response.Headers = sortedHeaders(response.Header)
		exchange.Response.ContentType = response.Header.Get("Content-Type")
		exchange.Response.ContentCodec = responseCodec
	})
	if response.Body == nil {
		server.finishResponseBody(id, path, responseCodec, nil, 0, false, nil)
		return response
	}

	var frameDecoder *connectFrameDecoder
	if path == runSSEPath {
		frameDecoder = newConnectFrameDecoder(
			runSSEResponseMessageType,
			responseCodec,
			server.config.MaxFrames,
			func(frame FrameView) { server.appendResponseFrame(id, frame) },
		)
	}
	response.Body = newCaptureReadCloser(
		response.Body,
		server.config.MaxCaptureBytes,
		func(chunk []byte) {
			if frameDecoder != nil {
				frameDecoder.Write(chunk)
			}
		},
		func(captured []byte, size int64, truncated bool, readErr error) {
			if frameDecoder != nil {
				frameDecoder.Close()
			}
			server.finishResponseBody(id, path, responseCodec, captured, size, truncated, readErr)
		},
	)
	return response
}

func (server *Server) finishRequestBody(id, path string, codec string, captured []byte, size int64, truncated bool, readErr error) {
	contentType := server.store.contentType(id, false)
	decodePayload := captured
	var contentDecodeErr error
	if truncated && decodesUnaryRequest(path) {
		contentDecodeErr = errors.New("请求正文超过抓取上限，无法完整解码")
	} else if decompressed, err := decompressIfNeeded(captured, codec); err != nil {
		if decodesUnaryRequest(path) {
			contentDecodeErr = err
		}
	} else {
		decodePayload = decompressed
	}
	decodedJSON, kind, requestID, decodeErr := "", "", "", contentDecodeErr
	if decodeErr == nil {
		decodedJSON, kind, requestID, decodeErr = decodeUnaryRequest(path, decodePayload)
	}
	// 部分客户端用 Connect unary envelope（5 字节头）承载 application/proto。
	if decodesUnaryRequest(path) && len(decodePayload) > 0 && (decodeErr != nil || decodedJSON == "") {
		if unwrapped, unwrapErr := maybeUnwrapConnectUnary(decodePayload, codec); unwrapErr == nil &&
			len(unwrapped) > 0 && len(unwrapped) != len(decodePayload) {
			altJSON, altKind, altID, altErr := decodeUnaryRequest(path, unwrapped)
			if altErr == nil && altJSON != "" {
				decodedJSON, kind, requestID, decodeErr = altJSON, altKind, altID, nil
				decodePayload = unwrapped
			}
		}
	}
	protoErr := decodeErr
	// Never treat Connect streams as plain text fallback.
	if decodedJSON == "" && runSSEMessageType(path, false) == "" {
		if fbJSON, fbKind, ok := fallbackDisplayBody(contentType, decodePayload); ok {
			decodedJSON, kind = fbJSON, fbKind
			decodeErr = nil
		}
	}
	var offlineFrames []FrameView
	if messageType := runSSEMessageType(path, false); messageType != "" && len(captured) > 0 {
		offlineFrames = decodeConnectFramesOffline(messageType, codec, server.maxFrames(), captured)
	}
	server.store.update(id, func(exchange *Exchange) {
		exchange.RequestBytes = size
		exchange.Request.Size = size
		exchange.Request.RawHex = rawHex(captured)
		exchange.Request.RawTruncated = truncated
		if decodedJSON != "" {
			exchange.Request.DecodedJSON = decodedJSON
		}
		if kind != "" {
			exchange.RequestKind = kind
		}
		if requestID != "" {
			exchange.RequestID = requestID
		}
		if decodeErr != nil {
			exchange.Request.DecodeError = decodeErr.Error()
		} else if protoErr != nil && decodedJSON != "" {
			// text/json fallback succeeded after proto failure — keep a soft warning.
			exchange.Request.DecodeError = "protobuf 解码失败，已回退为文本/JSON 展示: " + protoErr.Error()
		} else {
			exchange.Request.DecodeError = ""
		}
		// 官方 upstream 整包回灌不会走 MITM 流式拆帧；Frames 为空时补做 offline Connect 解码。
		if len(exchange.Request.Frames) == 0 && len(offlineFrames) > 0 {
			exchange.Request.Frames = offlineFrames
			if exchange.RequestKind == "" {
				exchange.RequestKind = firstNonEmptyFrameKind(offlineFrames)
			}
			if exchange.RequestID == "" {
				for _, frame := range offlineFrames {
					if frame.RequestID != "" {
						exchange.RequestID = frame.RequestID
						break
					}
				}
			}
		}
		// Unary / text / json：补一条合成 frame，避免 UI 默认 frames 页空白。
		if len(exchange.Request.Frames) == 0 && decodedJSON != "" {
			exchange.Request.Frames = []FrameView{syntheticUnaryFrame(path, kind, requestID, decodedJSON, len(decodePayload))}
		}
		if exchange.FrameCount == 0 {
			exchange.FrameCount = len(exchange.Request.Frames)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			exchange.Error = readErr.Error()
		}
	})
}

func requestContentCodec(path string, headers http.Header) string {
	if path == runSSEPath {
		return strings.TrimSpace(headers.Get("Connect-Content-Encoding"))
	}
	return strings.TrimSpace(headers.Get("Content-Encoding"))
}

func responseContentCodec(path string, headers http.Header) string {
	if path == runSSEPath {
		return strings.TrimSpace(headers.Get("Connect-Content-Encoding"))
	}
	if !decodesUnaryResponse(path) {
		if codec := strings.TrimSpace(headers.Get("Connect-Content-Encoding")); codec != "" {
			return codec
		}
	}
	return strings.TrimSpace(headers.Get("Content-Encoding"))
}

func (server *Server) finishResponseBody(id, path, codec string, captured []byte, size int64, truncated bool, readErr error) {
	contentType := server.store.contentType(id, true)
	decodePayload := captured
	var contentDecodeErr error
	if truncated && decodesUnaryResponse(path) {
		contentDecodeErr = errors.New("响应正文超过抓取上限，无法完整解码")
	} else if decompressed, err := decompressIfNeeded(captured, codec); err != nil {
		if decodesUnaryResponse(path) {
			contentDecodeErr = err
		}
	} else {
		decodePayload = decompressed
	}
	decodedJSON, kind, decodeErr := "", "", contentDecodeErr
	if decodeErr == nil {
		decodedJSON, kind, decodeErr = decodeUnaryResponse(path, decodePayload)
	}
	if decodesUnaryResponse(path) && len(decodePayload) > 0 && (decodeErr != nil || decodedJSON == "") {
		if unwrapped, unwrapErr := maybeUnwrapConnectUnary(decodePayload, codec); unwrapErr == nil &&
			len(unwrapped) > 0 && len(unwrapped) != len(decodePayload) {
			altJSON, altKind, altErr := decodeUnaryResponse(path, unwrapped)
			if altErr == nil && altJSON != "" {
				decodedJSON, kind, decodeErr = altJSON, altKind, nil
				decodePayload = unwrapped
			}
		}
	}
	protoErr := decodeErr
	var offlineFrames []FrameView
	if messageType := runSSEMessageType(path, true); messageType != "" && len(captured) > 0 {
		offlineFrames = decodeConnectFramesOffline(messageType, codec, server.maxFrames(), captured)
	}
	// Text/JSON fallback only for unary HTTP (not Connect RunSSE streams).
	if decodedJSON == "" && len(offlineFrames) == 0 && runSSEMessageType(path, true) == "" {
		if fbJSON, fbKind, ok := fallbackDisplayBody(contentType, decodePayload); ok {
			decodedJSON, kind = fbJSON, fbKind
			decodeErr = nil
		}
	}
	server.store.update(id, func(exchange *Exchange) {
		exchange.ResponseBytes = size
		exchange.Response.Size = size
		exchange.Response.RawHex = rawHex(captured)
		exchange.Response.RawTruncated = truncated
		if decodedJSON != "" {
			exchange.Response.DecodedJSON = decodedJSON
		}
		if kind != "" {
			exchange.ResponseKind = kind
		}
		if decodeErr != nil {
			exchange.Response.DecodeError = decodeErr.Error()
		} else if protoErr != nil && decodedJSON != "" {
			exchange.Response.DecodeError = "protobuf 解码失败，已回退为文本/JSON 展示: " + protoErr.Error()
		} else {
			exchange.Response.DecodeError = ""
		}
		// 官方 upstream RunSSE 先前只落 rawHex；这里补齐与本地 MITM 一致的 frames。
		if len(exchange.Response.Frames) == 0 && len(offlineFrames) > 0 {
			exchange.Response.Frames = offlineFrames
			if exchange.ResponseKind == "" {
				exchange.ResponseKind = firstNonEmptyFrameKind(offlineFrames)
			}
		}
		// Unary / text / json：合成 frame，避免响应默认 frames 页空白。
		if len(exchange.Response.Frames) == 0 && decodedJSON != "" {
			exchange.Response.Frames = []FrameView{syntheticUnaryFrame(path, kind, "", decodedJSON, len(decodePayload))}
		}
		if len(exchange.Response.Frames) > 0 {
			exchange.FrameCount = len(exchange.Response.Frames)
		} else if exchange.FrameCount == 0 {
			exchange.FrameCount = len(exchange.Request.Frames)
		}
		exchange.DurationMS = elapsedMS(exchange.StartedAt)
		exchange.State = "completed"
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			exchange.State = "error"
			exchange.Error = readErr.Error()
		}
	})
}

func (server *Server) maxFrames() int {
	if server != nil && server.config.MaxFrames > 0 {
		return server.config.MaxFrames
	}
	return defaultMaxFrames
}

func (server *Server) appendRequestFrame(id string, frame FrameView) {
	server.store.appendStreamingFrame(id, false, frame, server.config.MaxFrames)
}

func (server *Server) appendResponseFrame(id string, frame FrameView) {
	server.store.appendStreamingFrame(id, true, frame, server.config.MaxFrames)
}

func (server *Server) matchesHTTPRequest(request *http.Request) bool {
	if request == nil {
		return false
	}
	host := request.Host
	if request.URL != nil && request.URL.Host != "" {
		host = request.URL.Host
	}
	return server.matchesTargetHost(host)
}

func (server *Server) matchesTargetHost(host string) bool {
	return hostMatchesPatterns(host, server.config.targetHostPatterns)
}

// clientHopServer：经本地 MITM 时标 local，直连官方时标 official。
func (server *Server) clientHopServer() string {
	if strings.TrimSpace(server.config.UpstreamProxy) != "" {
		return ServerLocal
	}
	return ServerOfficial
}

// upstreamTransportOptions 配置出站：可选上游代理，并在走本地 MITM 时信任内置 CA。
func (server *Server) upstreamTransportOptions() (func(*http.Request) (*url.URL, error), *tls.Config, error) {
	upstream := strings.TrimSpace(server.config.UpstreamProxy)
	if upstream == "" {
		return nil, nil, nil
	}
	parsed, err := url.Parse(upstream)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 UpstreamProxy 失败：%w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, fmt.Errorf("UpstreamProxy 无效：%q", upstream)
	}

	rootCAs, err := x509.SystemCertPool()
	if err != nil || rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	if ok := rootCAs.AppendCertsFromPEM(certs.EmbeddedCACertPEM()); !ok {
		return nil, nil, errors.New("加载内置 CA 到信任池失败")
	}
	return http.ProxyURL(parsed), &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12}, nil
}

func exchangeID(context *goproxy.ProxyCtx) string {
	if context == nil {
		return ""
	}
	value, ok := context.UserData.(exchangeContext)
	if !ok {
		return ""
	}
	return value.id
}

func browserAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("调试界面监听地址无效：%w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("调试界面只能监听本机回环地址")
	}
	return nil
}
