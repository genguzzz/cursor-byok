package h2agentproxy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestBinaryBodyRoundTripPreservesBytes(t *testing.T) {
	payload := []byte{0x00, 0xff, 0xfe, 0x80, 0x00, 'p', 'b'}
	upstream := newHTTP2TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !bytes.Equal(body, payload) {
			http.Error(w, fmt.Sprintf("upstream got %x", body), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append([]byte{0x01}, payload...))
	}))

	proxy, client := startTestProxy(t, upstream)
	resp, err := client.Post("https://"+proxy.ListenAddr()+"/agent.v1.AgentService/Run", "application/connect+proto", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("client.Post: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	want := append([]byte{0x01}, payload...)
	if !bytes.Equal(got, want) {
		t.Fatalf("response body = %x, want %x", got, want)
	}

	reqCap, err := os.ReadFile(filepath.Join(proxy.CaptureDir(), "01_request.body.bin"))
	if err != nil {
		t.Fatalf("read request capture: %v", err)
	}
	if !bytes.Equal(reqCap, payload) {
		t.Fatalf("captured request = %x, want %x", reqCap, payload)
	}
	respCap, err := os.ReadFile(filepath.Join(proxy.CaptureDir(), "01_response.body.bin"))
	if err != nil {
		t.Fatalf("read response capture: %v", err)
	}
	if !bytes.Equal(respCap, want) {
		t.Fatalf("captured response = %x, want %x", respCap, want)
	}
}

func TestStreamingBidiDoesNotBufferEntireRequest(t *testing.T) {
	firstSeen := make(chan []byte, 1)
	upstream := newHTTP2TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		buf := make([]byte, 32)
		n, err := r.Body.Read(buf)
		if n == 0 && err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		chunk := append([]byte(nil), buf[:n]...)
		firstSeen <- chunk
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ECHO1"))
		flusher.Flush()
		rest, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte("ECHO2"))
		_, _ = w.Write(rest)
		flusher.Flush()
	}))

	proxy, client := startTestProxy(t, upstream)
	reader, writer := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, "https://"+proxy.ListenAddr()+"/agent.v1.AgentService/Run", reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	req.ContentLength = -1

	errCh := make(chan error, 1)
	var resp *http.Response
	go func() {
		var doErr error
		resp, doErr = client.Do(req)
		errCh <- doErr
	}()

	first := []byte{0x00, 0x01, 0xff}
	if _, err := writer.Write(first); err != nil {
		t.Fatalf("write first chunk: %v", err)
	}

	select {
	case got := <-firstSeen:
		if !bytes.Equal(got, first) {
			t.Fatalf("upstream first chunk = %x, want %x", got, first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not see first chunk before request completed")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("client.Do: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive streamed response headers")
	}
	if resp == nil {
		t.Fatal("missing response")
	}
	defer resp.Body.Close()

	head := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("read first echo: %v", err)
	}
	if string(head) != "ECHO1" {
		t.Fatalf("first echo = %q, want ECHO1", head)
	}

	second := []byte{0xaa, 0x00, 0xbb}
	if _, err := writer.Write(second); err != nil {
		t.Fatalf("write second chunk: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close request body: %v", err)
	}

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if !bytes.Equal(rest, append([]byte("ECHO2"), second...)) {
		t.Fatalf("rest = %q, want %q", rest, append([]byte("ECHO2"), second...))
	}
}

func TestLeafCertificateIncludesLoopbackIP(t *testing.T) {
	caPEM, keyPEM := mustGenerateTestCA(t)
	issuer, err := newCertIssuerFromBundle(append(caPEM, keyPEM...))
	if err != nil {
		t.Fatalf("newCertIssuerFromBundle: %v", err)
	}
	cert, err := issuer.certificateForNames("localhost", "agentn.global.api5.cursor.sh")
	if err != nil {
		t.Fatalf("certificateForNames: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("missing leaf")
	}
	foundLoopback := false
	for _, ip := range cert.Leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			foundLoopback = true
			break
		}
	}
	if !foundLoopback {
		t.Fatalf("leaf IPs = %v, want 127.0.0.1 SAN", cert.Leaf.IPAddresses)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{
		DNSName: "localhost",
		Roots:   roots,
	}); err != nil {
		t.Fatalf("verify localhost: %v", err)
	}
}

func TestSensitiveHeadersAreRedactedInCapture(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer secret-token")
	header.Set("Content-Type", "application/connect+proto")
	flat := flattenHeaders(header)
	if flat["Authorization"] != "<redacted>" {
		t.Fatalf("Authorization = %q, want redacted", flat["Authorization"])
	}
	if flat["Content-Type"] != "application/connect+proto" {
		t.Fatalf("Content-Type unexpectedly changed: %q", flat["Content-Type"])
	}
}

func TestListenAddrIsConfigurable(t *testing.T) {
	upstream := newHTTP2TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	proxy := mustStartProxy(t, Config{
		ListenAddr:         "127.0.0.1:0",
		UpstreamHost:       hostPort(upstream.URL),
		UpstreamServerName: "127.0.0.1",
		UpstreamInsecure:   true,
		CaptureDir:         t.TempDir(),
		EmbeddedCACertPEM:  testCACertPEM,
		EmbeddedCAKeyPEM:   testCAKeyPEM,
	})
	host, port, err := net.SplitHostPort(proxy.ListenAddr())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	if host != "127.0.0.1" || port == "8443" || port == "0" {
		t.Fatalf("listen addr = %s, want ephemeral 127.0.0.1 port", proxy.ListenAddr())
	}
}

func newHTTP2TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func startTestProxy(t *testing.T, upstream *httptest.Server) (*Server, *http.Client) {
	t.Helper()
	proxy := mustStartProxy(t, Config{
		ListenAddr:         "127.0.0.1:0",
		UpstreamHost:       hostPort(upstream.URL),
		UpstreamServerName: "127.0.0.1",
		UpstreamInsecure:   true,
		CaptureDir:         t.TempDir(),
		EmbeddedCACertPEM:  testCACertPEM,
		EmbeddedCAKeyPEM:   testCAKeyPEM,
	})
	return proxy, newH2Client(t, proxy)
}

func mustStartProxy(t *testing.T, config Config) *Server {
	t.Helper()
	if len(config.EmbeddedCACertPEM) == 0 {
		config.EmbeddedCACertPEM = testCACertPEM
		config.EmbeddedCAKeyPEM = testCAKeyPEM
	}
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	return server
}

func newH2Client(t *testing.T, proxy *Server) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(proxy.CAPEM()) {
		t.Fatal("append proxy CA")
	}
	transport := &http2.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: "localhost",
			NextProtos: []string{http2.NextProtoTLS},
			MinVersion: tls.VersionTLS12,
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func hostPort(rawURL string) string {
	u := mustURLHost(rawURL)
	return u
}

func mustURLHost(rawURL string) string {
	trimmed := rawURL
	if len(trimmed) > 8 && trimmed[:8] == "https://" {
		trimmed = trimmed[8:]
	}
	return trimmed
}

var (
	testCACertPEM []byte
	testCAKeyPEM  []byte
)

func TestMain(m *testing.M) {
	certPEM, keyPEM := mustGenerateTestCA(nil)
	testCACertPEM, testCAKeyPEM = certPEM, keyPEM
	os.Exit(m.Run())
}

func mustGenerateTestCA(t *testing.T) ([]byte, []byte) {
	if t != nil {
		t.Helper()
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "h2-agent-proxy-test-ca",
			Organization: []string{"h2-agent-proxy tests"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}
