package expand

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/Ranjandas/parker/internal/spec"
)

// Files the TLS setup writes into each node's directory.
const (
	CAFile      = "ca.pem"
	CertFile    = "server.pem"
	CertKeyFile = "server-key.pem"
	HTTPSPort   = 8501
)

// certValidity is long enough for any lab and short enough to look odd if
// it leaks somewhere real.
const certValidity = 90 * 24 * time.Hour

type ca struct {
	lab     string
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM string
	keyPEM  string
}

func tlsOn(d *spec.Inheritable, dc *spec.Datacenter) bool {
	on := false
	if d.TLS != nil {
		on = *d.TLS
	}
	if dc.TLS != nil {
		on = *dc.TLS
	}
	return on
}

func serial() (*big.Int, error) {
	b := make([]byte, 16)
	if _, err := Rand.Read(b); err != nil {
		return nil, err
	}
	b[0] &= 0x7f
	return new(big.Int).SetBytes(b), nil
}

func keyPEM(k *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

// newCA makes the lab's CA. It signs the servers' certificates and is
// Consul's Connect root, so auto-encrypt client certificates chain to it too.
func newCA(lab string) (*ca, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), Rand)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now().Add(-time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "Parker CA " + lab, Organization: []string{"Parker"}},
		NotBefore:             now,
		NotAfter:              now.Add(certValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(Rand, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return nil, err
	}
	return &ca{lab: lab, cert: cert, key: key, keyPEM: kp,
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}, nil
}

// serverCert issues a certificate for a server of dc: server.<dc>.consul
// (what verify_server_hostname checks), its names under `parker proxy`,
// localhost, and the given IPs.
func (c *ca) serverCert(node, dc string, ips []string) (certPEM, keyPEMStr string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), Rand)
	if err != nil {
		return "", "", err
	}
	sn, err := serial()
	if err != nil {
		return "", "", err
	}
	ipAddrs := []net.IP{net.ParseIP("127.0.0.1")}
	for _, ip := range ips {
		if p := net.ParseIP(ip); p != nil {
			ipAddrs = append(ipAddrs, p)
		}
	}
	now := time.Now().Add(-time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "server." + dc + ".consul", Organization: []string{"Parker"}},
		DNSNames: []string{
			"server." + dc + ".consul", node + ".server." + dc + ".consul", "localhost",
			node + "." + dc + "." + c.lab + ".parker", dc + "-" + node + "." + c.lab + ".parker",
		},
		IPAddresses: ipAddrs,
		NotBefore:   now,
		NotAfter:    now.Add(certValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(Rand, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return "", "", err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), kp, nil
}

// serverTLS is the server agent's TLS config: mutual TLS for RPC, HTTPS
// only for the API, auto-encrypt for clients, and the lab CA as the
// Connect root.
func (c *ca) serverTLS(cfg map[string]any, files map[string]string, node, dc string, ips []string) error {
	cert, key, err := c.serverCert(node, dc, ips)
	if err != nil {
		return fmt.Errorf("%s: %w", node, err)
	}
	files[CAFile] = c.certPEM
	files[CertFile] = cert
	files[CertKeyFile] = key
	cfg["tls"] = map[string]any{
		"defaults": map[string]any{
			"ca_file":         VarNodeDir + "/" + CAFile,
			"cert_file":       VarNodeDir + "/" + CertFile,
			"key_file":        VarNodeDir + "/" + CertKeyFile,
			"verify_incoming": true,
			"verify_outgoing": true,
		},
		"internal_rpc": map[string]any{"verify_server_hostname": true},
		"https":        map[string]any{"verify_incoming": false},
	}
	cfg["auto_encrypt"] = map[string]any{"allow_tls": true}
	cfg["ports"] = map[string]any{"http": -1, "https": HTTPSPort}
	cfg["connect"] = map[string]any{
		"enabled":     true,
		"ca_provider": "consul",
		"ca_config":   map[string]any{"private_key": c.keyPEM, "root_cert": c.certPEM},
	}
	return nil
}

// clientTLS has the client agent get its certificate from the servers.
func (c *ca) clientTLS(cfg map[string]any, files map[string]string) {
	files[CAFile] = c.certPEM
	cfg["tls"] = map[string]any{
		"defaults": map[string]any{
			"ca_file":         VarNodeDir + "/" + CAFile,
			"verify_outgoing": true,
		},
		"internal_rpc": map[string]any{"verify_server_hostname": true},
	}
	cfg["auto_encrypt"] = map[string]any{"tls": true}
	cfg["ports"] = map[string]any{"http": -1, "https": HTTPSPort}
}

// ReissueServerCerts re-signs every server certificate in f whose IP
// addresses fix changes, for the new addresses, with the same key and the
// lab CA. `parker up` calls it after moving the lab to free subnets: the
// certificates are opaque PEM, so rewriting the lab's addresses can't reach
// the IPs inside them.
func ReissueServerCerts(f *spec.File, fix func(string) string) error {
	for name, n := range f.Nodes {
		certPEM, ok := n.Files[CertFile]
		if !ok || n.Files[CAFile] == "" {
			continue
		}
		cert, err := parseCertPEM(certPEM)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", name, CertFile, err)
		}
		ips, changed := make([]net.IP, 0, len(cert.IPAddresses)), false
		for _, ip := range cert.IPAddresses {
			moved := net.ParseIP(fix(ip.String()))
			if moved == nil {
				moved = ip
			}
			if !moved.Equal(ip) {
				changed = true
			}
			ips = append(ips, moved)
		}
		if !changed {
			continue
		}
		caCert, err := parseCertPEM(n.Files[CAFile])
		if err != nil {
			return fmt.Errorf("%s: %s: %w", name, CAFile, err)
		}
		caKey, err := caKeyOf(n.Files["consul.json"])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		sn, err := serial()
		if err != nil {
			return err
		}
		tmpl := &x509.Certificate{
			SerialNumber: sn,
			Subject:      cert.Subject,
			DNSNames:     cert.DNSNames,
			IPAddresses:  ips,
			NotBefore:    cert.NotBefore,
			NotAfter:     cert.NotAfter,
			KeyUsage:     cert.KeyUsage,
			ExtKeyUsage:  cert.ExtKeyUsage,
		}
		der, err := x509.CreateCertificate(Rand, tmpl, caCert, cert.PublicKey, caKey)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		n.Files[CertFile] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	return nil
}

func parseCertPEM(s string) (*x509.Certificate, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("no certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

// caKeyOf reads the lab CA's key from a server's agent config, where it is
// the Connect root (see serverTLS).
func caKeyOf(consulJSON string) (*ecdsa.PrivateKey, error) {
	var cfg struct {
		Connect struct {
			CAConfig struct {
				PrivateKey string `json:"private_key"`
			} `json:"ca_config"`
		} `json:"connect"`
	}
	if err := json.Unmarshal([]byte(consulJSON), &cfg); err != nil {
		return nil, fmt.Errorf("consul.json: %w", err)
	}
	b, _ := pem.Decode([]byte(cfg.Connect.CAConfig.PrivateKey))
	if b == nil {
		return nil, fmt.Errorf("consul.json: no lab CA key to re-sign the server certificate with")
	}
	return x509.ParseECPrivateKey(b.Bytes)
}
