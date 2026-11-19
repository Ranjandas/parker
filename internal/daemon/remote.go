package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/Ranjandas/parker/internal/api/parkerv1/parkerv1connect"
)

// StateDir holds the daemon's long-lived files (TLS cert and key, API
// token): PARKER_STATE_DIR, default /var/lib/parker. Unlike the run dir it
// survives reboots, so clients keep trusting the same certificate.
func StateDir() string {
	if d := os.Getenv("PARKER_STATE_DIR"); d != "" {
		return d
	}
	return "/var/lib/parker"
}

func CertPath() string  { return filepath.Join(StateDir(), "tls", "cert.pem") }
func keyPath() string   { return filepath.Join(StateDir(), "tls", "key.pem") }
func TokenPath() string { return filepath.Join(StateDir(), "token") }

// Token returns the API token for remote clients, creating it on first use.
func Token() (string, error) {
	if b, err := os.ReadFile(TokenPath()); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(TokenPath(), []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// ensureCert loads the daemon's TLS certificate, creating a self-signed one
// (ECDSA P-256, 10 years) for the host's names and addresses if missing.
func ensureCert(extraSANs []string) (tls.Certificate, error) {
	if c, err := tls.LoadX509KeyPair(CertPath(), keyPath()); err == nil {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "parker daemon " + host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed: clients use it as their CA
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipn.IP)
			}
		}
	}
	for _, san := range extraSANs {
		if ip := net.ParseIP(san); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if san != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, san)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(filepath.Dir(CertPath()), 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath(), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(CertPath(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(CertPath(), keyPath())
}

// Fingerprint is the SHA-256 of the certificate, for checking out of band.
func Fingerprint(c tls.Certificate) string {
	sum := sha256.Sum256(c.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// tokenAuth rejects calls without `Authorization: Bearer <token>`, as a
// proper Connect/gRPC error so clients can show the message.
type tokenAuth struct{ want []byte }

var errBadToken = errors.New("missing or wrong parker token (set PARKER_TOKEN; on the daemon host: parker daemon credentials)")

func (a tokenAuth) ok(h http.Header) bool {
	return subtle.ConstantTimeCompare([]byte(h.Get("Authorization")), a.want) == 1
}

func (a tokenAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !a.ok(req.Header()) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errBadToken)
		}
		return next(ctx, req)
	}
}

func (a tokenAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a tokenAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if !a.ok(conn.RequestHeader()) {
			return connect.NewError(connect.CodeUnauthenticated, errBadToken)
		}
		return next(ctx, conn)
	}
}

// Target says how to reach a daemon.
type Target struct {
	Host     string // unix:///path or https://host:port; "" = local socket
	Token    string // for https
	CAFile   string // PEM to trust for https; "" = system roots
	Insecure bool   // skip certificate verification (testing only)
}

// IsRemote reports whether the target is a TCP daemon rather than a local
// socket.
func (t Target) IsRemote() bool { return strings.HasPrefix(t.Host, "https://") }

// Dial returns an API client for the target.
func (t Target) Dial() (parkerv1connect.ParkerServiceClient, error) {
	switch {
	case t.Host == "":
		return Client(SocketPath()), nil
	case strings.HasPrefix(t.Host, "unix://"):
		return Client(strings.TrimPrefix(t.Host, "unix://")), nil
	case strings.HasPrefix(t.Host, "https://"):
	default:
		return nil, fmt.Errorf("PARKER_HOST %q: want unix:///path or https://host:port", t.Host)
	}
	u, err := url.Parse(t.Host)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{InsecureSkipVerify: t.Insecure} //nolint:gosec // explicit opt-in
	if t.CAFile != "" {
		pemBytes, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("%s: no certificates", t.CAFile)
		}
		cfg.RootCAs = pool
	}
	if t.Token == "" {
		return nil, errors.New("PARKER_TOKEN is required for a remote daemon (on the daemon host: parker daemon credentials)")
	}
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	hc := &http.Client{Transport: bearer{t.Token, tr}}
	return parkerv1connect.NewParkerServiceClient(hc, "https://"+u.Host, connect.WithGRPC()), nil
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// serveTCP serves the API over TLS on addr, requiring the token.
func serveTCP(ctx context.Context, addr string, sans []string, s *Server, log func(string, ...any)) (*http.Server, <-chan error, error) {
	cert, err := ensureCert(sans)
	if err != nil {
		return nil, nil, fmt.Errorf("tls: %w", err)
	}
	tok, err := Token()
	if err != nil {
		return nil, nil, fmt.Errorf("token: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(parkerv1connect.NewParkerServiceHandler(s, connect.WithInterceptors(tokenAuth{want: []byte("Bearer " + tok)})))
	srv := &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}},
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ServeTLS(ln, "", "") }()
	log("remote API on https://%s (TLS cert %s, sha256 %s); token in %s",
		ln.Addr(), CertPath(), Fingerprint(cert), TokenPath())
	return srv, errc, nil
}
