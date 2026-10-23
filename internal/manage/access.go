package manage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/auth"
)

type AccessConfig struct {
	Host      string     `json:"host"`
	Port      int        `json:"port"`
	Completed bool       `json:"completed"`
	CloseAt   *time.Time `json:"closeAt,omitempty"`
}

func (m *Manager) AuthStore() *auth.Store {
	return &auth.Store{Path: filepath.Join(filepath.Dir(m.P.State), "auth.json")}
}
func (m *Manager) APISocket() string {
	return filepath.Join(filepath.Dir(m.P.Lock), "gost-deploy-api.sock")
}
func (m *Manager) accessPath() string { return filepath.Join(filepath.Dir(m.P.State), "access.json") }
func (m *Manager) accessCertPath() string {
	return filepath.Join(filepath.Dir(m.P.State), "access.pem")
}
func (m *Manager) Access() (v AccessConfig, e error) {
	b, e := os.ReadFile(m.accessPath())
	if os.IsNotExist(e) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	if e == nil {
		// Older releases stored the literal as entered.
		v.Host = canonicalHost(v.Host)
		e = validateAccess(v)
	}
	return
}
func validateAccess(v AccessConfig) error {
	if v.Port < 1024 || v.Port > 65535 || v.Port == 18080 {
		return errors.New("管理 HTTPS 端口无效")
	}
	if ip := net.ParseIP(v.Host); ip != nil {
		// Browsers send the canonical literal in Host; AccessOrigin compares it exactly.
		if ip.String() != v.Host {
			return errors.New("管理访问 IP 必须使用规范格式")
		}
	} else {
		if e := ValidateTLSOptions(TLSOptions{Domain: v.Host, Email: "setup@example.com", AcceptTOS: true}); e != nil && v.Host != "localhost" {
			return errors.New("管理访问地址必须是 IP 或域名")
		}
	}
	return nil
}

// canonicalHost accepts bracketed or non-canonical IPv6 literals from the CLI.
func canonicalHost(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}
func (v AccessConfig) URL() string {
	return "https://" + net.JoinHostPort(v.Host, strconv.Itoa(v.Port))
}

// ConfigureAccess is separate from the core transaction: failures leave the
// installed forwarding service usable and can be retried without replacing it.
func (m *Manager) ConfigureAccess(ctx context.Context, host string, port int) (code, fingerprint string, err error) {
	created := false
	err = m.Locked(func() error {
		v, e := m.Access()
		if e != nil {
			return e
		}
		if port == 0 {
			port = v.Port
			if port == 0 {
				port = 18443
			}
		}
		host = canonicalHost(host)
		if host == "" {
			host = v.Host
			if host == "" {
				host = "localhost"
			}
		}
		if v.Port == 0 || host != v.Host || port != v.Port {
			candidate := v
			candidate.Host = host
			candidate.Port = port
			if e = validateAccess(candidate); e != nil {
				return e
			}
			if port != v.Port {
				l, e := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
				if e != nil {
					return errors.New("管理 HTTPS 端口被占用")
				}
				l.Close()
			}
			v = candidate
			created = true
			if e = writeJSON(m.accessPath(), v, 0600, -1); e != nil {
				return e
			}
		}
		if _, e = m.AccessCertificate(); e != nil {
			return e
		}
		code, e = m.AuthStore().Claim(false)
		if e != nil {
			return e
		}
		cert, e := m.AccessCertificate()
		if e != nil {
			return e
		}
		sum := sha256.Sum256(cert.Certificate[0])
		fingerprint = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return
	}
	if created || m.accessHealth(ctx) != nil {
		_, err = m.sys(ctx, "restart", "gost-deploy-api.service")
	}
	if err == nil {
		err = m.accessHealth(ctx)
	}
	if err == nil {
		err = os.Remove(m.P.Token)
		if os.IsNotExist(err) {
			err = nil
		}
	}
	return
}
func (m *Manager) AccessCertificate() (*tls.Certificate, error) {
	v, e := m.Access()
	if e != nil || v.Port == 0 {
		return nil, errors.New("尚未配置管理 HTTPS")
	}
	b, e := os.ReadFile(m.accessCertPath())
	if e == nil {
		cert, e := tls.X509KeyPair(b, b)
		if e == nil {
			leaf, e := x509.ParseCertificate(cert.Certificate[0])
			if e == nil && time.Until(leaf.NotAfter) > 30*24*time.Hour && leaf.VerifyHostname(v.Host) == nil {
				return &cert, nil
			}
		}
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return nil, e
	}
	t := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "RelayHarbor management"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(v.Host); ip != nil {
		t.IPAddresses = []net.IP{ip}
	} else {
		t.DNSNames = []string{v.Host}
	}
	der, e := x509.CreateCertificate(rand.Reader, &t, &t, &key.PublicKey, key)
	if e != nil {
		return nil, e
	}
	priv, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return nil, e
	}
	b = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})...)
	if e = Atomic(m.accessCertPath(), b, 0600, -1); e != nil {
		return nil, e
	}
	cert, e := tls.X509KeyPair(b, b)
	return &cert, e
}
func (m *Manager) AccessOrigin(host string, secure bool) string {
	v, e := m.Access()
	if e == nil && secure && v.Port != 0 && host == net.JoinHostPort(v.Host, strconv.Itoa(v.Port)) {
		return v.URL()
	}
	t, e := m.loadTLS()
	if e == nil && t != nil && t.Enabled && (host == t.Domain || host == t.Domain+":443") {
		return "https://" + host
	}
	return ""
}
func (m *Manager) AccessHandoff() error {
	return m.Locked(func() error {
		v, e := m.Access()
		if e != nil || v.Port == 0 {
			return e
		}
		if v.CloseAt == nil {
			at := time.Now().Add(5 * time.Minute)
			v.CloseAt = &at
			return writeJSON(m.accessPath(), v, 0600, -1)
		}
		return nil
	})
}
func (m *Manager) SetupComplete(expected string) error {
	return m.Locked(func() error {
		s, e := m.Load(false)
		if e != nil {
			return e
		}
		if s.Revision != expected {
			return ErrSetupRevision
		}
		v, e := m.Access()
		if e != nil {
			return e
		}
		if v.Completed {
			return ErrSetupCompleted
		}
		v.Completed = true
		return writeJSON(m.accessPath(), v, 0600, -1)
	})
}
func (m *Manager) ResetAuth(ctx context.Context) (string, error) {
	code, e := m.AuthStore().Claim(true)
	if e != nil {
		return "", e
	}
	reopened := false
	e = m.Locked(func() error {
		v, e := m.Access()
		if e != nil {
			return e
		}
		t, e := m.loadTLS()
		if e != nil {
			return e
		}
		if v.Port != 0 && v.CloseAt != nil && (t == nil || !t.Enabled) {
			v.CloseAt = nil
			reopened = true
			return writeJSON(m.accessPath(), v, 0600, -1)
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	if reopened {
		if _, e = m.sys(ctx, "restart", "gost-deploy-api.service"); e != nil {
			return "", e
		}
	}
	return code, nil
}
func (m *Manager) SetupAddress() string {
	v, e := m.Access()
	if e != nil {
		return ""
	}
	t, e := m.loadTLS()
	if e == nil && t != nil && t.Enabled {
		return "https://" + t.Domain
	}
	return v.URL()
}
func (m *Manager) AccessFiles() []string {
	return []string{m.AuthStore().Path, m.AuthStore().Path + ".lock", m.accessPath(), m.accessCertPath()}
}
func validJobID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil && !strings.Contains(s, "/")
}

func (m *Manager) reservedAccessAddress() string {
	v, e := m.Access()
	if e != nil || v.Port == 0 {
		return ""
	}
	return net.JoinHostPort("", strconv.Itoa(v.Port))
}

func (m *Manager) accessHealth(ctx context.Context) error {
	if e := m.apiHealth(ctx); e != nil {
		return e
	}
	v, e := m.Access()
	if e != nil {
		return e
	}
	if v.CloseAt != nil && time.Now().After(*v.CloseAt) {
		return nil
	}
	certificate, e := m.AccessCertificate()
	if e != nil {
		return e
	}
	leaf, e := x509.ParseCertificate(certificate.Certificate[0])
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(v.Port)))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, e := http.NewRequestWithContext(ctx, "GET", v.URL()+"/api/v1/auth/status", nil)
		if e != nil {
			return e
		}
		r, e := client.Do(req)
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("管理 HTTPS 页面未通过就绪检查")
}
