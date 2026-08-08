package h2agentproxy

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

type certIssuer struct {
	caCert *x509.Certificate
	caKey  crypto.PrivateKey
	caPEM  []byte

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

func newCertIssuerFromBundle(bundle []byte) (*certIssuer, error) {
	caCert, caKey, caPEM, err := parseCABundle(bundle)
	if err != nil {
		return nil, err
	}
	return &certIssuer{
		caCert: caCert,
		caKey:  caKey,
		caPEM:  caPEM,
		cache:  make(map[string]*tls.Certificate),
	}, nil
}

func parseCABundle(data []byte) (*x509.Certificate, crypto.PrivateKey, []byte, error) {
	var (
		cert    *x509.Certificate
		certPEM []byte
		key     crypto.PrivateKey
	)
	rest := data
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		switch block.Type {
		case "CERTIFICATE":
			parsed, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("parse certificate: %w", err)
			}
			if parsed.IsCA || cert == nil {
				cert = parsed
				certPEM = pem.EncodeToMemory(block)
			}
		case "PRIVATE KEY", "EC PRIVATE KEY", "RSA PRIVATE KEY":
			parsed, err := parsePrivateKey(block)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("parse private key: %w", err)
			}
			key = parsed
		}
	}
	if cert == nil {
		return nil, nil, nil, fmt.Errorf("CA certificate not found in bundle")
	}
	if key == nil {
		return nil, nil, nil, fmt.Errorf("CA private key not found in bundle")
	}
	return cert, key, certPEM, nil
}

func parsePrivateKey(block *pem.Block) (crypto.PrivateKey, error) {
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unknown key type: %s", block.Type)
	}
}

func (issuer *certIssuer) CAPEM() []byte {
	return append([]byte(nil), issuer.caPEM...)
}

func (issuer *certIssuer) certificateForNames(serverName string, extraNames ...string) (*tls.Certificate, error) {
	cacheKey := serverName
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if cert, ok := issuer.cache[cacheKey]; ok {
		return cert, nil
	}

	dnsNames := uniqueNonEmpty(append([]string{serverName, "localhost"}, extraNames...))
	ipAddresses := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if ip := net.ParseIP(serverName); ip != nil {
		ipAddresses = append([]net.IP{ip}, ipAddresses...)
		dnsNames = filterDNSNames(dnsNames, serverName)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   serverName,
			Organization: []string{"Cursor CLI H2 Proxy"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
		BasicConstraintsValid: true,
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, issuer.caCert, &priv.PublicKey, issuer.caKey)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if len(issuer.caPEM) > 0 {
		certPEM = append(certPEM, issuer.caPEM...)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if parsed, err := x509.ParseCertificate(certDER); err == nil {
		tlsCert.Leaf = parsed
	}
	issuer.cache[cacheKey] = &tlsCert
	return &tlsCert, nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		if net.ParseIP(value) != nil {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func filterDNSNames(names []string, skipIP string) []string {
	out := names[:0]
	for _, name := range names {
		if name == skipIP {
			continue
		}
		out = append(out, name)
	}
	return out
}
