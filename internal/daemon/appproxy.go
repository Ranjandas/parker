package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Ranjandas/parker/internal/expand"
	"github.com/Ranjandas/parker/internal/fake"
	"github.com/Ranjandas/parker/internal/lab"
	"github.com/Ranjandas/parker/internal/netsetup"
	"github.com/Ranjandas/parker/internal/supervisor"
)

// isFakePort tells whether port on node is a fake service (run:
// fake-service) in a running lab.
func isFakePort(labName, node string, port int) bool {
	low, err := lab.LoadTopology(labName)
	if err != nil {
		return false
	}
	for _, f := range lab.FakeInfos(low, node) {
		if f.Port == port {
			return true
		}
	}
	return false
}

// AppPrefix is where the daemon proxies HTTP to a port inside a lab:
// /app/<lab>/<node>/<scheme>/<port>/<path>. The web UI sends app UIs
// (the Consul UI, a service's page) here; the CLI's `parker ui` reaches it
// through its API proxy, so a remote daemon works the same.
const AppPrefix = "/app/"

// appTarget is one parsed app proxy path.
type appTarget struct {
	lab, node, scheme string
	port              int
	rest, rawRest     string // the app's own path, unescaped and escaped
}

func parseAppPath(u *url.URL) (appTarget, error) {
	var t appTarget
	esc := strings.TrimPrefix(u.EscapedPath(), AppPrefix)
	parts := strings.SplitN(esc, "/", 5)
	if len(parts) < 4 {
		return t, errors.New("want /app/<lab>/<node>/<scheme>/<port>/<path>")
	}
	t.lab, t.node, t.scheme = parts[0], parts[1], parts[2]
	port, err := strconv.Atoi(parts[3])
	if err != nil || port < 1 || port > 65535 {
		return t, fmt.Errorf("bad port %q", parts[3])
	}
	t.port = port
	if t.scheme != "http" && t.scheme != "https" {
		return t, fmt.Errorf("scheme %q: want http or https", t.scheme)
	}
	t.rawRest = "/"
	if len(parts) == 5 {
		t.rawRest = "/" + parts[4]
	}
	if t.rest, err = url.PathUnescape(t.rawRest); err != nil {
		return t, err
	}
	return t, nil
}

// labCA is the lab's CA for HTTPS upstreams (Consul with TLS), from any
// node's rendered files. nil when the lab has none.
func labCA(name string) (*x509.CertPool, error) {
	low, err := lab.LoadTopology(name)
	if err != nil {
		return nil, err
	}
	for _, n := range low.Nodes {
		if pem, ok := n.Files[expand.CAFile]; ok {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM([]byte(pem)) {
				return pool, nil
			}
		}
	}
	return nil, nil
}

// verifyChain checks a server's certificate against the lab CA only: the
// proxy dials an IP, which lab certificates (named server.<dc>.consul and
// the like) need not carry.
func verifyChain(pool *x509.CertPool) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("no server certificate")
		}
		inter := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter})
		return err
	}
}

// AppProxy proxies HTTP (and WebSocket upgrades) to ports inside labs. It
// resolves targets with ResolveAddress, so it reaches only lab networks.
// Requests arrive already authenticated (the socket's permissions, or the
// TCP listener's token); the token never goes on to the app.
func AppProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, err := parseAppPath(r.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if t.scheme == "http" && strings.HasPrefix(t.rest, fake.UIControlPath) {
			fakeUIControl(w, r, t)
			return
		}
		addr, err := ResolveAddress(net.JoinHostPort(t.node, strconv.Itoa(t.port)), t.lab)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "tcp", addr)
		}
		if t.scheme == "http" && isFakePort(t.lab, t.node, t.port) {
			// A fake behind a transparent proxy only takes mesh traffic
			// on its node IP; its own page is reached on the node's
			// loopback, which the proxy leaves alone.
			in := supervisor.DialerIn(netsetup.NetnsPath(t.lab, t.node))
			loop := net.JoinHostPort("127.0.0.1", strconv.Itoa(t.port))
			dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				return in(ctx, "tcp", loop)
			}
		}
		tr := &http.Transport{
			DialContext:           dial,
			ResponseHeaderTimeout: time.Minute, // Consul blocking queries wait up to 5m, but in chunks
		}
		if t.scheme == "https" {
			pool, err := labCA(t.lab)
			if err != nil || pool == nil {
				http.Error(w, fmt.Sprintf("lab %s has no CA for https: %v", t.lab, err), http.StatusBadGateway)
				return
			}
			//nolint:gosec // verified against the lab CA in VerifyConnection
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, VerifyConnection: verifyChain(pool)}
		}
		defer tr.CloseIdleConnections()
		up := &url.URL{Scheme: t.scheme, Host: addr}
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme, pr.Out.URL.Host = up.Scheme, up.Host
				pr.Out.URL.Path, pr.Out.URL.RawPath = t.rest, ""
				if t.rawRest != t.rest {
					pr.Out.URL.RawPath = t.rawRest
				}
				pr.Out.Host = addr
				pr.Out.Header.Del("Authorization")
				// The browser-facing host, for apps that build absolute links.
				for _, h := range []string{"X-Forwarded-Host", "X-Forwarded-Proto"} {
					if v := pr.In.Header.Get(h); v != "" {
						pr.Out.Header.Set(h, v)
					}
				}
			},
			Transport:     tr,
			FlushInterval: -1,
			// Redirects to the app's own address stay on the browser's origin.
			ModifyResponse: func(resp *http.Response) error {
				if loc := resp.Header.Get("Location"); loc != "" {
					if u, err := url.Parse(loc); err == nil && u.Host == addr {
						u.Scheme, u.Host = "", ""
						resp.Header.Set("Location", u.String())
					}
				}
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, fmt.Sprintf("%s:%d in %s: %v", t.node, t.port, t.lab, err), http.StatusBadGateway)
			},
		}
		rp.ServeHTTP(w, r)
	})
}

// requireToken lets through only requests that carry the API token.
func requireToken(tok string, next http.Handler) http.Handler {
	a := tokenAuth{want: []byte("Bearer " + tok)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.ok(r.Header) {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
