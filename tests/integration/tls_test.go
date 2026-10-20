package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"gitee.com/kenny3755/gost_deploy/internal/api"
	"gitee.com/kenny3755/gost_deploy/internal/config"
	"gitee.com/kenny3755/gost_deploy/internal/manage"
	"gitee.com/kenny3755/gost_deploy/internal/testkit"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pebbleServer(t *testing.T) (string, *http.Client) {
	t.Helper()
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated ACME test"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, certPEM, 0600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600)
	path := filepath.Join(dir, "pebble.json")
	write(t, path, map[string]any{"pebble": map[string]any{"listenAddress": "127.0.0.1:14000", "managementListenAddress": "127.0.0.1:15000", "certificate": certPath, "privateKey": keyPath, "httpPort": 80, "tlsPort": 443, "retryAfter": map[string]int{"authz": 1, "order": 1}, "profiles": map[string]any{"default": map[string]any{"description": "integration", "validityPeriod": 60}}}})
	cmd := exec.Command("/fixtures/pebble", "-config", path)
	cmd.Env = append(os.Environ(), "PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0", "PEBBLE_AUTHZREUSE=0")
	log, e := os.Create(filepath.Join(dir, "pebble.log"))
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(log.Name())
			t.Log(string(b))
		}
	})
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 0; i < 100; i++ {
		r, e := client.Get("https://localhost:14000/dir")
		if e == nil {
			r.Body.Close()
			break
		}
		if i == 99 {
			t.Fatal(e)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// PrivateTmp in the production renewal unit deliberately hides this test's /tmp.
	trustPath := "/var/lib/gost-deploy/pebble-test-root.pem"
	if e = os.WriteFile(trustPath, certPEM, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SSL_CERT_FILE", trustPath)
	sys(t, "set-environment", "SSL_CERT_FILE="+trustPath)
	t.Cleanup(func() { sys(t, "unset-environment", "SSL_CERT_FILE") })
	response, e := client.Get("https://localhost:15000/roots/0")
	if e != nil {
		t.Fatal(e)
	}
	root, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || !pool.AppendCertsFromPEM(root) {
		t.Fatal("root CA", e)
	}
	if e = os.WriteFile("/var/lib/gost-deploy/pebble-serving-root.pem", root, 0600); e != nil {
		t.Fatal(e)
	}
	return "https://localhost:14000/dir", client
}

func TestTLSDeployment(t *testing.T) {
	isolated(t)
	if _, e := os.Stat("/fixtures/pebble"); e != nil {
		t.Fatal("missing verified Pebble fixture", e)
	}
	dir := t.TempDir()
	c := config.Initial("127.0.0.1:1080", false)
	path := filepath.Join(dir, "config.json")
	write(t, path, c)
	run(t, "install", "--config", path, "--archive", "/artifacts/gost_3.3.0_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.3.0.txt")
	retain := false
	t.Cleanup(func() {
		if !retain {
			run(t, "uninstall", "--purge")
		}
	})
	ca, client := pebbleServer(t)
	m := manage.New()
	o := manage.TLSOptions{Domain: "gost-panel.test", Email: "admin@example.com", AcceptTOS: true, Server: ca, Archive: "/artifacts/lego_v5.5.2_linux_amd64.tar.gz", Checksums: "/artifacts/lego_5.5.2_checksums.txt"}
	fakeBin := filepath.Join(t.TempDir(), "nginx")
	if e := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0755); e != nil {
		t.Fatal(e)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(fakeBin)+":"+oldPath)
	if e := m.TLSSetup(context.Background(), o); e == nil {
		t.Fatal("foreign nginx executable accepted")
	}
	t.Setenv("PATH", oldPath)
	// Reject foreign nginx before downloading or changing services.
	os.MkdirAll("/etc/nginx", 0755)
	if e := m.TLSSetup(context.Background(), o); e == nil {
		t.Fatal("foreign nginx accepted")
	}
	os.Remove("/etc/nginx")
	// Even a package-manager failure leaves a resumable ownership record.
	m.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "apt-get" {
			return nil, errors.New("injected apt interruption")
		}
		return manage.Run(ctx, name, args...)
	}
	if e := m.TLSSetup(context.Background(), o); e == nil {
		t.Fatal("apt failure swallowed")
	}
	provisioned, e := os.ReadFile("/etc/gost-deploy/tls.json")
	if e != nil {
		t.Fatal(e)
	}
	var partial manage.TLSConfig
	if json.Unmarshal(provisioned, &partial) != nil || !partial.Provisioning || partial.Enabled {
		t.Fatal("missing resumable state")
	}
	m.Exec = manage.Run
	trust, e := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	if e != nil {
		t.Fatal(e)
	}
	servingRoot, e := os.ReadFile("/var/lib/gost-deploy/pebble-serving-root.pem")
	if e != nil {
		t.Fatal(e)
	}
	run(t, "uninstall", "--purge")
	if _, e := os.Stat("/etc/gost-deploy/tls.json"); !os.IsNotExist(e) {
		t.Fatal("half-installed TLS state was not purged", e)
	}
	run(t, "install", "--config", path, "--archive", "/artifacts/gost_3.3.0_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.3.0.txt")
	if e = os.WriteFile(os.Getenv("SSL_CERT_FILE"), trust, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile("/var/lib/gost-deploy/pebble-serving-root.pem", servingRoot, 0600); e != nil {
		t.Fatal(e)
	}
	echo := testkit.Echo(t)
	held := testkit.Connect(t, c, echo)
	defer held.Close()
	beforePID := pid(t, "gost")
	setupErr := func() error {
		old := syscall.Umask(0077)
		defer syscall.Umask(old)
		return m.TLSSetup(context.Background(), o)
	}()
	if e := setupErr; e != nil {
		t.Fatal(e)
	}
	// All requests validate the actual CA chain and server hostname, including through nginx.
	tokenBytes, _ := os.ReadFile("/var/lib/gost-deploy/api-token")
	token := strings.TrimSpace(string(tokenBytes))
	call := func(method, path, rev string, body string) (int, http.Header, []byte) {
		t.Helper()
		r, e := http.NewRequest(method, "https://gost-panel.test"+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		if rev != "" {
			r.Header.Set("If-Match", `"`+rev+`"`)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		return resp.StatusCode, resp.Header, b
	}
	code, _, html := call("GET", "/", "", "")
	if code != 200 || !strings.Contains(string(html), "GOST 控制台") {
		t.Fatal("console over HTTPS", code)
	}
	code, headers, _ := call("GET", "/api/v1/config", "", "")
	if code != 200 || headers.Get("ETag") == "" {
		t.Fatal("config/ETag through nginx")
	}
	rev := strings.Trim(headers.Get("ETag"), `"`)
	code, _, _ = call("PUT", "/api/v1/active-profile", rev, `{"name":"direct"}`)
	if code != 200 {
		t.Fatal("If-Match stripped", code)
	}
	code, _, _ = call("PUT", "/api/v1/active-profile", "stale", `{"name":"direct"}`)
	if code != 409 {
		t.Fatal("stale revision accepted", code)
	}
	r, e := client.Get("https://gost-panel.test/api/v1/status")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("unauthenticated API exposed")
	}
	r, e = client.Get("http://gost-panel.test/")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 308 || r.Header.Get("Location") != "https://gost-panel.test/" {
		t.Fatal("HTTP redirect")
	}
	s, e := m.TLSStatus(context.Background())
	if e != nil || !s.Enabled || !s.NginxActive || !s.TimerActive || !s.TimerEnabled || s.NotAfter == nil {
		t.Fatal(s, e)
	}
	firstExpiry := *s.NotAfter
	if e = m.TLSSetup(context.Background(), o); e != nil {
		t.Fatal("idempotent setup", e)
	}
	afterAnyTLSRenewal := anyTLSAcceptance(t, m, c, client)
	// Use a short-lived test certificate to exercise a real due renewal with default lego policy.
	waitUntil := firstExpiry.Add(-20 * time.Second)
	if delay := time.Until(waitUntil); delay > 0 {
		time.Sleep(delay)
	}
	_, rev, e = api.Client(context.Background(), "/var/lib/gost-deploy/api-token", "GET", "/config", nil, "")
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = api.Client(context.Background(), "/var/lib/gost-deploy/api-token", "POST", "/tls/renew", map[string]any{}, rev)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 200; i++ {
		s, e = m.TLSStatus(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !s.Running && s.LastResult != nil && s.LastResult.Operation == "renew" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s.LastResult == nil || s.LastResult.Error != "" || !s.LastResult.Deployed || s.NotAfter == nil || !s.NotAfter.After(firstExpiry) {
		t.Fatal("automatic worker renewal", s, s.LastResult)
	}
	code, _, _ = call("GET", "/api/v1/tls", "", "")
	if code != 200 {
		t.Fatal("renewed HTTPS unavailable")
	}
	if e = m.TLSStart(context.Background(), "reload"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		s, _ = m.TLSStatus(context.Background())
		if !s.Running && s.LastResult != nil && s.LastResult.Operation == "reload" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.LastResult == nil || s.LastResult.Error != "" {
		t.Fatal("reload failed", s)
	}
	afterAnyTLSRenewal()
	if testkit.Payload(held) != nil || pid(t, "gost") != beforePID {
		t.Fatal("TLS work interrupted SOCKS")
	}
	if e = m.TLSDisable(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Get("https://gost-panel.test/"); e == nil {
		t.Fatal("disabled HTTPS still served")
	}
	// Retained ACME state supports enabling again without taking over a foreign installation.
	if e = m.TLSSetup(context.Background(), o); e != nil {
		t.Fatal("re-enable", e)
	}
	run(t, "anytls", "enable")
	for _, path := range []string{"/etc/gost-deploy/tls.json", "/var/lib/gost-deploy/tls/live/privkey.pem"} {
		st, e := os.Stat(path)
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatal("private permissions", path, e)
		}
	}
	serialized, _ := json.Marshal(s)
	t.Log("PASS real nginx + lego + HTTP-01 + CA-verified HTTPS + systemd renewal + held SOCKS", string(serialized))
	if os.Getenv("GOST_TLS_BOOT") == "prepare" {
		retain = true
	}
}

func TestTLSBootVerify(t *testing.T) {
	isolated(t)
	if os.Getenv("GOST_TLS_BOOT") != "verify" {
		t.Skip("TLS boot verification only")
	}
	t.Cleanup(func() { run(t, "uninstall", "--purge") })
	verifyAnyTLSBoot(t)
	m := manage.New()
	s, e := m.TLSStatus(context.Background())
	if e != nil || !s.Enabled || !s.TimerActive || !s.TimerEnabled || !s.NginxActive || s.NextCheck == "" {
		t.Fatal("TLS boot state", s, e)
	}
	root, e := os.ReadFile("/var/lib/gost-deploy/pebble-serving-root.pem")
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(root)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	r, e := client.Get("https://gost-panel.test/")
	if e != nil {
		t.Fatal("HTTPS after boot", e)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	t.Log("PASS HTTPS and persistent renewal timer after container reboot")
}
