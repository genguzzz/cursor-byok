package h2agentproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

const defaultUpstreamHost = "agentn.global.api5.cursor.sh"

type Config struct {
	ListenAddr         string
	UpstreamHost       string
	UpstreamServerName string
	UpstreamInsecure   bool
	CABundlePath       string
	CaptureDir         string
	EmbeddedCACertPEM  []byte
	EmbeddedCAKeyPEM   []byte
}

type captureContextKey struct{}

type streamCapture struct {
	id          int
	startedAt   time.Time
	reqTee      *teeReadCloser
	respTee     *teeReadCloser
	reqHeaders  string
	respHeaders string
}

type Server struct {
	config   Config
	issuer   *certIssuer
	captures *captureStore
	proxy    *httputil.ReverseProxy
	httpSrv  *http.Server
	listener net.Listener
}

func (config Config) normalized() (Config, error) {
	if strings.TrimSpace(config.ListenAddr) == "" {
		config.ListenAddr = "127.0.0.1:8443"
	}
	if strings.TrimSpace(config.UpstreamHost) == "" {
		config.UpstreamHost = defaultUpstreamHost
	}
	host, _, err := splitHostPort(config.UpstreamHost)
	if err != nil {
		return config, err
	}
	if strings.TrimSpace(config.UpstreamServerName) == "" {
		config.UpstreamServerName = host
	}
	if strings.TrimSpace(config.CaptureDir) == "" {
		config.CaptureDir = filepath.Join("captures", time.Now().Format("20060102_150405"))
	}
	return config, nil
}

func NewServer(config Config) (*Server, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}

	issuer, err := loadIssuer(config)
	if err != nil {
		return nil, err
	}
	captures, err := newCaptureStore(config.CaptureDir)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(config.CaptureDir, "ca.crt"), issuer.CAPEM(), 0o644); err != nil {
		return nil, fmt.Errorf("write capture CA: %w", err)
	}

	upstreamURL, err := url.Parse("https://" + config.UpstreamHost)
	if err != nil {
		return nil, fmt.Errorf("parse upstream: %w", err)
	}

	transport := &http2.Transport{
		DisableCompression: true,
		TLSClientConfig: &tls.Config{
			ServerName:         config.UpstreamServerName,
			InsecureSkipVerify: config.UpstreamInsecure,
			NextProtos:         []string{http2.NextProtoTLS},
			MinVersion:         tls.VersionTLS12,
		},
	}

	server := &Server{
		config:   config,
		issuer:   issuer,
		captures: captures,
	}
	server.proxy = &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(upstreamURL)
			req.Out.Host = upstreamURL.Host
			if req.In.ContentLength < 0 {
				req.Out.ContentLength = -1
			}
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			return server.captureResponse(resp)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			id := 0
			if capture, ok := r.Context().Value(captureContextKey{}).(*streamCapture); ok {
				id = capture.id
			}
			log.Printf("[%02d] upstream error: %v", id, err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	return server, nil
}

func loadIssuer(config Config) (*certIssuer, error) {
	if strings.TrimSpace(config.CABundlePath) != "" {
		bundle, err := os.ReadFile(config.CABundlePath)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle: %w", err)
		}
		return newCertIssuerFromBundle(bundle)
	}
	if len(config.EmbeddedCACertPEM) == 0 || len(config.EmbeddedCAKeyPEM) == 0 {
		return nil, fmt.Errorf("CA bundle path is required, or embed a CA cert+key")
	}
	bundle := append(append([]byte{}, config.EmbeddedCACertPEM...), config.EmbeddedCAKeyPEM...)
	return newCertIssuerFromBundle(bundle)
}

func (server *Server) CaptureDir() string    { return server.config.CaptureDir }
func (server *Server) ListenAddr() string    { return server.config.ListenAddr }
func (server *Server) UpstreamHost() string  { return server.config.UpstreamHost }
func (server *Server) CAPEM() []byte         { return server.issuer.CAPEM() }

func (server *Server) Start() error {
	tlsCfg := &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			serverName := hello.ServerName
			if serverName == "" {
				serverName = "localhost"
			}
			return server.issuer.certificateForNames(serverName, server.config.UpstreamServerName)
		},
		NextProtos: []string{http2.NextProtoTLS},
		MinVersion: tls.VersionTLS12,
	}
	httpSrv := &http.Server{
		Addr:              server.config.ListenAddr,
		Handler:           http.HandlerFunc(server.serveHTTP),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       10 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          log.Default(),
	}
	if err := http2.ConfigureServer(httpSrv, &http2.Server{}); err != nil {
		return fmt.Errorf("configure h2: %w", err)
	}
	listener, err := net.Listen("tcp", server.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", server.config.ListenAddr, err)
	}
	server.httpSrv = httpSrv
	server.listener = listener
	server.config.ListenAddr = listener.Addr().String()
	go func() {
		tlsListener := tls.NewListener(listener, tlsCfg)
		if err := httpSrv.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
			log.Printf("h2 proxy serve: %v", err)
		}
	}()
	return nil
}

func (server *Server) Close(ctx context.Context) error {
	if server.httpSrv == nil {
		return nil
	}
	return server.httpSrv.Shutdown(ctx)
}

func (server *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	id := server.captures.nextID()
	capture := &streamCapture{id: id, startedAt: time.Now()}
	reqHeaderFile := requestCaptureName(id, "headers.json")
	reqBodyFile := requestCaptureName(id, "body.bin")
	_ = server.captures.writeJSON(reqHeaderFile, map[string]any{
		"method":  request.Method,
		"path":    request.URL.Path,
		"query":   request.URL.RawQuery,
		"host":    request.Host,
		"headers": flattenHeaders(request.Header),
	})
	capture.reqHeaders = reqHeaderFile

	if request.Body != nil {
		bodyFile, err := server.captures.createBodyFile(reqBodyFile)
		if err != nil {
			log.Printf("[%02d] create request capture: %v", id, err)
			http.Error(writer, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		capture.reqTee = newTeeReadCloser(request.Body, bodyFile)
		request.Body = capture.reqTee
		if request.ContentLength == 0 && request.Header.Get("Content-Length") == "" {
			request.ContentLength = -1
		}
	}

	log.Printf("[%02d] REQ %s %s", id, request.Method, request.URL.RequestURI())
	ctx := context.WithValue(request.Context(), captureContextKey{}, capture)
	server.proxy.ServeHTTP(writer, request.WithContext(ctx))
	if capture.reqTee != nil {
		capture.reqTee.Wait(2 * time.Second)
	}
	if capture.respTee != nil {
		capture.respTee.Wait(2 * time.Second)
	}
	base := fmt.Sprintf("%02d", id)
	if err := decodeCapturePair(
		server.CaptureDir(),
		base,
		filepath.Join(server.CaptureDir(), requestCaptureName(id, "body.bin")),
		filepath.Join(server.CaptureDir(), requestCaptureName(id, "headers.json")),
		filepath.Join(server.CaptureDir(), responseCaptureName(id, "body.bin")),
	); err != nil {
		log.Printf("[%02d] decode: %v", id, err)
	}
	log.Printf("[%02d] DONE %s (%s)", id, request.URL.Path, time.Since(capture.startedAt).Truncate(time.Millisecond))
}

func (server *Server) captureResponse(resp *http.Response) error {
	capture, _ := resp.Request.Context().Value(captureContextKey{}).(*streamCapture)
	if capture == nil {
		return nil
	}
	respHeaderFile := responseCaptureName(capture.id, "headers.json")
	respBodyFile := responseCaptureName(capture.id, "body.bin")
	_ = server.captures.writeJSON(respHeaderFile, map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
		"trailer": flattenHeaders(resp.Trailer),
	})
	capture.respHeaders = respHeaderFile
	if resp.Body == nil {
		log.Printf("[%02d] RESP %d (no body)", capture.id, resp.StatusCode)
		return nil
	}
	bodyFile, err := server.captures.createBodyFile(respBodyFile)
	if err != nil {
		return err
	}
	capture.respTee = newTeeReadCloser(resp.Body, bodyFile)
	resp.Body = capture.respTee
	log.Printf("[%02d] RESP %d streaming", capture.id, resp.StatusCode)
	return nil
}

func splitHostPort(hostport string) (string, string, error) {
	if hostport == "" {
		return "", "", fmt.Errorf("empty upstream host")
	}
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		host, port, _ := net.SplitHostPort(hostport)
		return host, port, nil
	}
	if strings.Count(hostport, ":") > 0 && !strings.Contains(hostport, "]") {
		if ip := net.ParseIP(hostport); ip == nil {
			return "", "", fmt.Errorf("invalid upstream host %q", hostport)
		}
	}
	return hostport, "443", nil
}
