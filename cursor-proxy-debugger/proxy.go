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

// Server runs the HTTPS debugging proxy and its local web UI.
type Server struct {
	config      Config
	certManager *certs.Manager
	store       *exchangeStore
	counter     atomic.Uint64
	proxyServer *http.Server
	uiServer    *http.Server
	proxyLn     net.Listener
	uiLn        net.Listener
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
	server.proxyLn = proxyListener
	server.uiLn = uiListener
	go func() { _ = server.proxyServer.Serve(proxyListener) }()
	go func() { _ = server.uiServer.Serve(uiListener) }()
	return nil
}

// Close stops both listeners.
func (server *Server) Close(ctx context.Context) error {
	server.runMu.Lock()
	proxyServer := server.proxyServer
	uiServer := server.uiServer
	server.proxyLn = nil
	server.uiLn = nil
	server.runMu.Unlock()
	var errorsList []error
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
	proxy.Tr = &http.Transport{
		Proxy:                 proxyFunc,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
	}

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
	decodePayload := captured
	var contentDecodeErr error
	if decodesUnaryRequest(path) && truncated {
		contentDecodeErr = errors.New("请求正文超过抓取上限，无法完整解码")
	} else if decodesUnaryRequest(path) && codec != "" && !strings.EqualFold(codec, "identity") {
		decodePayload, contentDecodeErr = decompressPayload(captured, codec)
	}
	decodedJSON, kind, requestID, decodeErr := "", "", "", contentDecodeErr
	if decodeErr == nil {
		decodedJSON, kind, requestID, decodeErr = decodeUnaryRequest(path, decodePayload)
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
	decodePayload := captured
	var contentDecodeErr error
	if decodesUnaryResponse(path) && truncated {
		contentDecodeErr = errors.New("响应正文超过抓取上限，无法完整解码")
	} else if decodesUnaryResponse(path) && codec != "" && !strings.EqualFold(codec, "identity") {
		decodePayload, contentDecodeErr = decompressPayload(captured, codec)
	}
	decodedJSON, kind, decodeErr := "", "", contentDecodeErr
	if decodeErr == nil {
		decodedJSON, kind, decodeErr = decodeUnaryResponse(path, decodePayload)
	}
	var offlineFrames []FrameView
	if messageType := runSSEMessageType(path, true); messageType != "" && len(captured) > 0 {
		offlineFrames = decodeConnectFramesOffline(messageType, codec, server.maxFrames(), captured)
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
		}
		// 官方 upstream RunSSE 先前只落 rawHex；这里补齐与本地 MITM 一致的 frames。
		if len(exchange.Response.Frames) == 0 && len(offlineFrames) > 0 {
			exchange.Response.Frames = offlineFrames
			exchange.FrameCount = len(offlineFrames)
			if exchange.ResponseKind == "" {
				exchange.ResponseKind = firstNonEmptyFrameKind(offlineFrames)
			}
		} else {
			exchange.FrameCount = len(exchange.Response.Frames)
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
	server.store.update(id, func(exchange *Exchange) {
		if len(exchange.Request.Frames) < server.config.MaxFrames {
			exchange.Request.Frames = append(exchange.Request.Frames, frame)
		}
		if frame.Kind != "" {
			exchange.RequestKind = frame.Kind
		}
		if frame.RequestID != "" {
			exchange.RequestID = frame.RequestID
		}
	})
}

func (server *Server) appendResponseFrame(id string, frame FrameView) {
	server.store.update(id, func(exchange *Exchange) {
		if len(exchange.Response.Frames) < server.config.MaxFrames {
			exchange.Response.Frames = append(exchange.Response.Frames, frame)
		}
		exchange.FrameCount = len(exchange.Response.Frames)
		if frame.Kind != "" && frame.Kind != "end_stream" {
			exchange.ResponseKind = frame.Kind
		}
		if frame.Error != "" {
			exchange.Response.DecodeError = frame.Error
		}
	})
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
