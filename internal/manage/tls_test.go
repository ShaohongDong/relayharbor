package manage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func tlsFixture(t *testing.T) (*Manager, *TLSConfig) {
	t.Helper()
	m := fixtureManager(t)
	m.TLSProbe = func(context.Context) error { return nil }
	p := m.tlsPaths()
	for _, dir := range []string{filepath.Dir(p.lock), p.root, p.units} {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	client := filepath.Join(filepath.Dir(m.P.Releases), "acme/client/lego")
	if e := Atomic(client, []byte("verified fixture"), 0700, -1); e != nil {
		t.Fatal(e)
	}
	h, _ := Digest(client)
	c := &TLSConfig{Schema: 1, Domain: "panel.example.com", Email: "admin@example.com", Server: LetsEncrypt, Client: Release{Binary: client, SHA256: h}, Resources: map[string]string{}}
	for _, item := range []struct{ name, text string }{{tlsRenewUnit, tlsUnit("renew")}, {tlsReloadUnit, tlsUnit("reload")}, {tlsTimerUnit, tlsTimer}} {
		path := filepath.Join(p.units, item.name)
		if e := Atomic(path, []byte(item.text), 0644, -1); e != nil {
			t.Fatal(e)
		}
		c.Resources[path] = hashText([]byte(item.text))
	}
	if e := m.writeTLSSite(c, m.tlsSite(c, false, "")); e != nil {
		t.Fatal(e)
	}
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return tlsTestSystem(m, name, args...)
	}
	return m, c
}

func candidateCert(t *testing.T, m *Manager, c *TLSConfig, domain string, expiry time.Time) []byte {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domain}, Issuer: pkix.Name{CommonName: "fixture CA"}, DNSNames: []string{domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	raw, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	path := m.candidateTLS(c)
	if e = Atomic(path+".crt", cert, 0600, -1); e != nil {
		t.Fatal(e)
	}
	if e = Atomic(path+".key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600, -1); e != nil {
		t.Fatal(e)
	}
	return cert
}

func enableFixture(t *testing.T, m *Manager, c *TLSConfig) {
	t.Helper()
	candidateCert(t, m, c, c.Domain, time.Now().Add(48*time.Hour))
	if e := m.setupTLSCertificate(context.Background(), c, TLSOptions{Domain: c.Domain, Email: c.Email}); e != nil {
		t.Fatal(e)
	}
}

func TestTLSOptions(t *testing.T) {
	valid := TLSOptions{Domain: "panel.example.com", Email: "admin@example.com", AcceptTOS: true}
	if e := ValidateTLSOptions(valid); e != nil {
		t.Fatal(e)
	}
	for _, domain := range []string{"*.example.com", "127.0.0.1", "https://example.com", "Example.com", "example.com;", "example.com\ninclude /tmp/x;", "-a.example.com", "a..com", "example.com/", "localhost"} {
		o := valid
		o.Domain = domain
		if ValidateTLSOptions(o) == nil {
			t.Fatal("accepted", domain)
		}
	}
	for _, email := range []string{"bad", "User <a@example.com>", "a@example.com\n"} {
		o := valid
		o.Email = email
		if ValidateTLSOptions(o) == nil {
			t.Fatal("accepted email", email)
		}
	}
	valid.AcceptTOS = false
	if ValidateTLSOptions(valid) == nil {
		t.Fatal("missing TOS accepted")
	}
}

func TestTLSInitialFailureAndRetry(t *testing.T) {
	m, c := tlsFixture(t)
	original, _ := os.ReadFile(m.tlsPaths().site)
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == m.P.Control {
			return nil, errors.New("interrupted")
		}
		return tlsTestSystem(m, name, args...)
	}
	if e := m.setupTLSCertificate(context.Background(), c, TLSOptions{Domain: c.Domain, Email: c.Email}); e == nil {
		t.Fatal("failure swallowed")
	}
	after, _ := os.ReadFile(m.tlsPaths().site)
	saved, _ := m.loadTLS()
	if saved.Enabled || string(original) != string(after) || strings.Contains(string(after), "proxy_pass") {
		t.Fatal("exposed management before signing")
	}
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return tlsTestSystem(m, name, args...)
	}
	enableFixture(t, m, c)
	s, e := m.TLSStatus(context.Background())
	if e != nil || !s.Enabled || s.NotAfter == nil || !s.Expiring || s.LastResult == nil || !s.LastResult.Deployed {
		t.Fatal(s, e)
	}
}

func TestTLSReloadFailureRetainsCertificateAndRetriesWithoutACME(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	old, _ := os.Readlink(filepath.Join(m.tlsPaths().root, "live"))
	newCert := candidateCert(t, m, c, c.Domain, time.Now().Add(72*time.Hour))
	failOnce := true
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "reload" && failOnce {
			failOnce = false
			return nil, errors.New("reload failed")
		}
		return tlsTestSystem(m, name, args...)
	}
	if e := m.TLSRun(context.Background(), "renew"); e == nil {
		t.Fatal("reload failure swallowed")
	}
	current, _ := os.Readlink(filepath.Join(m.tlsPaths().root, "live"))
	if current != old {
		t.Fatal("failed reload replaced certificate")
	}
	acmeCalls := 0
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == m.P.Control {
			acmeCalls++
			return nil, errors.New("CA unavailable")
		}
		return tlsTestSystem(m, name, args...)
	}
	if e := m.TLSRun(context.Background(), "renew"); e == nil {
		t.Fatal("CA failure not reported")
	}
	deployed, _ := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/fullchain.pem"))
	if string(deployed) != string(newCert) || acmeCalls != 1 {
		t.Fatal("pending certificate was not deployed before ACME retry")
	}
	s, e := m.TLSStatus(context.Background())
	if e != nil || s.LastResult == nil || !s.LastResult.Deployed || s.LastResult.Acquired || s.LastResult.Error == "" {
		t.Fatal(s, e)
	}
}

func TestTLSDomainChangeRollback(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	oldSite, _ := os.ReadFile(m.tlsPaths().site)
	oldLive, _ := os.Readlink(filepath.Join(m.tlsPaths().root, "live"))
	copy := *c
	copy.Domain = "new.example.com"
	candidateCert(t, m, &copy, copy.Domain, time.Now().Add(72*time.Hour))
	// Fail after signing and switching, when enabling the timer.
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 2 && args[0] == "enable" && args[2] == tlsTimerUnit {
			return nil, errors.New("timer failure")
		}
		return tlsTestSystem(m, name, args...)
	}
	if e := m.setupTLSCertificate(context.Background(), c, TLSOptions{Domain: copy.Domain, Email: copy.Email}); e == nil {
		t.Fatal("failure swallowed")
	}
	saved, _ := m.loadTLS()
	site, _ := os.ReadFile(m.tlsPaths().site)
	live, _ := os.Readlink(filepath.Join(m.tlsPaths().root, "live"))
	if saved.Domain != "panel.example.com" || string(site) != string(oldSite) || live != oldLive {
		t.Fatal("domain change did not restore all state")
	}
}

func TestTLSRejectsInvalidCandidateAndTampering(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	old, _ := os.Readlink(filepath.Join(m.tlsPaths().root, "live"))
	for _, tt := range []struct {
		domain string
		expiry time.Time
	}{{"other.example.com", time.Now().Add(time.Hour)}, {c.Domain, time.Now().Add(-time.Minute)}} {
		candidateCert(t, m, c, tt.domain, tt.expiry)
		if m.deployTLS(context.Background(), c) == nil {
			t.Fatal("bad cert accepted")
		}
		current, _ := os.Readlink(filepath.Join(m.tlsPaths().root, "live"))
		if current != old {
			t.Fatal("bad cert activated")
		}
	}
	if e := os.WriteFile(m.tlsPaths().site, []byte("foreign"), 0644); e != nil {
		t.Fatal(e)
	}
	if m.TLSStart(context.Background(), "renew") == nil || m.TLSDisable(context.Background(), true) == nil {
		t.Fatal("tampered deployment accepted")
	}
}

func TestTLSConcurrencyAndDisable(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	if e := m.tlsLocked(func() error {
		if !errors.Is(m.TLSStart(context.Background(), "renew"), ErrTLSBusy) {
			t.Fatal("parallel renew allowed")
		}
		if !errors.Is(m.TLSDisable(context.Background(), false), ErrTLSBusy) {
			t.Fatal("parallel disable allowed")
		}
		// Proxy mutations use a different lock.
		return m.Locked(func() error { return nil })
	}); e != nil {
		t.Fatal(e)
	}
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) == 5 && args[0] == "show" && args[1] == tlsRenewUnit && args[3] == "ActiveState" {
			return []byte("activating\n"), nil
		}
		return tlsTestSystem(m, name, args...)
	}
	if !errors.Is(m.TLSStart(context.Background(), "reload"), ErrTLSBusy) {
		t.Fatal("queued renew not protected")
	}
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return tlsTestSystem(m, name, args...)
	}
	if e := m.TLSDisable(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if exists(m.tlsPaths().site) {
		t.Fatal("disabled site left active")
	}
	if e := m.verifyTLS(mustTLS(t, m)); e != nil {
		t.Fatal(e)
	}
	if e := m.TLSDisable(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if exists(m.tlsPaths().root) || exists(m.tlsPaths().config) {
		t.Fatal("purge left keys/state")
	}
}

func TestTLSExpiredPendingCertificateDoesNotBlockRenewal(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	candidateCert(t, m, c, c.Domain, time.Now().Add(-time.Minute))
	calls := 0
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == m.P.Control {
			calls++
			candidateCert(t, m, c, c.Domain, time.Now().Add(72*time.Hour))
		}
		return tlsTestSystem(m, name, args...)
	}
	if e := m.TLSRun(context.Background(), "renew"); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("expired pending certificate prevented ACME")
	}
	s, _ := m.TLSStatus(context.Background())
	if s.LastResult == nil || !s.LastResult.Acquired || !s.LastResult.Deployed {
		t.Fatal(s.LastResult)
	}
	if e := m.TLSRun(context.Background(), "reload"); e != nil {
		t.Fatal(e)
	}
	// A successful check before renewal is due must not claim to have issued a new certificate.
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return tlsTestSystem(m, name, args...)
	}
	if e := m.TLSRun(context.Background(), "renew"); e != nil {
		t.Fatal(e)
	}
	s, _ = m.TLSStatus(context.Background())
	if s.LastResult.Acquired || s.LastResult.Deployed {
		t.Fatal("no-op renewal misreported", s.LastResult)
	}
}

func TestTLSDisableFailureRestoresSiteAndTimer(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	before, _ := os.ReadFile(m.tlsPaths().site)
	restoredTimer := false
	failOnce := true
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 1 && args[0] == "reload" && failOnce {
			failOnce = false
			return nil, errors.New("injected reload failure")
		}
		if name == "systemctl" && len(args) > 1 && args[0] == "start" && args[1] == tlsTimerUnit {
			restoredTimer = true
		}
		return tlsTestSystem(m, name, args...)
	}
	if m.TLSDisable(context.Background(), false) == nil {
		t.Fatal("disable failure swallowed")
	}
	after, _ := os.ReadFile(m.tlsPaths().site)
	saved := mustTLS(t, m)
	if string(before) != string(after) || !saved.Enabled || !restoredTimer {
		t.Fatal("disable failure lost active deployment")
	}
}

func TestTLSRefusesExternalUnit(t *testing.T) {
	m, c := tlsFixture(t)
	enableFixture(t, m, c)
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) == 5 && args[0] == "show" && args[3] == "DropInPaths" {
			return []byte("/etc/systemd/system/foreign.conf"), nil
		}
		return tlsTestSystem(m, name, args...)
	}
	if m.TLSStart(context.Background(), "renew") == nil || m.TLSRun(context.Background(), "reload") == nil || m.TLSDisable(context.Background(), true) == nil {
		t.Fatal("external drop-in accepted")
	}
	s, e := m.TLSStatus(context.Background())
	if e != nil || s.Warning == "" {
		t.Fatal("external unit not reported", s, e)
	}
}

func TestTLSProvisioningCleanup(t *testing.T) {
	for _, phase := range []string{"before-apt", "partial-units", "challenge-site", "foreign"} {
		t.Run(phase, func(t *testing.T) {
			m, c := tlsFixture(t)
			p := m.tlsPaths()
			c.Provisioning = true
			if phase == "before-apt" || phase == "partial-units" {
				os.Remove(p.site)
				for _, u := range []string{tlsRenewUnit, tlsReloadUnit, tlsTimerUnit} {
					if phase == "partial-units" && u == tlsRenewUnit {
						continue
					}
					os.Remove(filepath.Join(p.units, u))
				}
				c.Resources = map[string]string{}
			}
			if phase == "foreign" {
				os.WriteFile(p.site, []byte("foreign site"), 0644)
			}
			if e := writeJSON(p.config, c, 0600, -1); e != nil {
				t.Fatal(e)
			}
			e := m.TLSDisable(context.Background(), true)
			if phase == "foreign" {
				if e == nil {
					t.Fatal("foreign provisioning file removed")
				}
				b, _ := os.ReadFile(p.site)
				if string(b) != "foreign site" {
					t.Fatal("foreign site changed")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			for _, path := range []string{p.config, p.root, p.site, c.Client.Binary} {
				if exists(path) {
					t.Fatal("partial TLS resource survived purge", path)
				}
			}
		})
	}
}

func TestTLSWebrootPermissions(t *testing.T) {
	m, _ := tlsFixture(t)
	e := func() error { old := syscall.Umask(0077); defer syscall.Umask(old); return m.tlsWebroot() }()
	if e != nil {
		t.Fatal(e)
	}
	root := m.tlsPaths().webroot
	for _, path := range []string{root, filepath.Join(root, ".well-known"), filepath.Join(root, ".well-known/acme-challenge")} {
		st, e := os.Stat(path)
		if e != nil || st.Mode().Perm() != 0755 {
			t.Fatal("nginx cannot read challenge under strict umask", path, e)
		}
	}
	target := t.TempDir()
	before, e := os.Stat(target)
	if e != nil {
		t.Fatal(e)
	}
	leaf := filepath.Join(root, ".well-known/acme-challenge")
	os.Remove(leaf)
	os.Symlink(target, leaf)
	if m.tlsWebroot() == nil {
		t.Fatal("symlinked webroot accepted")
	}
	st, _ := os.Stat(target)
	if st.Mode().Perm() != before.Mode().Perm() {
		t.Fatal("changed external directory permissions")
	}
}

func TestLEGOReleaseVerification(t *testing.T) {
	name := "lego_v" + LEGOVersion + "_linux_" + runtime.GOARCH + ".tar.gz"
	archive := filepath.Join("../../.cache/releases", name)
	sums := filepath.Join("../../.cache/releases", "lego_"+LEGOVersion+"_checksums.txt")
	if !exists(archive) {
		t.Skip("make fetch supplies verified lego fixtures")
	}
	m := fixtureManager(t)
	r, e := m.prepareLEGO(context.Background(), archive, sums)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := m.Exec(context.Background(), r.Binary, "--version"); e != nil || !strings.Contains(string(b), LEGOVersion) {
		t.Fatal("verified client execution", string(b), e)
	}
	if _, e = m.prepareLEGO(context.Background(), archive, sums); e != nil {
		t.Fatal("cache reuse", e)
	}
	outside := filepath.Join(t.TempDir(), "lego")
	b, e := os.ReadFile(r.Binary)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(outside, b, 0700)
	os.Remove(r.Binary)
	os.Symlink(outside, r.Binary)
	if _, e = m.prepareLEGO(context.Background(), archive, sums); e == nil {
		t.Fatal("symlink cache accepted")
	}
	bad := filepath.Join(t.TempDir(), name)
	os.WriteFile(bad, []byte("truncated archive"), 0600)
	if _, e = m.prepareLEGO(context.Background(), bad, sums); e == nil {
		t.Fatal("tampered archive accepted")
	}
}

func mustTLS(t *testing.T, m *Manager) *TLSConfig {
	t.Helper()
	c, e := m.loadTLS()
	if e != nil || c == nil {
		t.Fatal(e)
	}
	return c
}

func tlsTestSystem(m *Manager, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) == 5 && args[0] == "show" && args[3] == "FragmentPath" {
		return []byte(filepath.Join(m.tlsPaths().units, args[1])), nil
	}
	return nil, nil
}

func TestTLSSiteIPv6Listeners(t *testing.T) {
	m, c := tlsFixture(t)
	dual, v4 := m.tlsSiteFor(c, true, "", true), m.tlsSiteFor(c, true, "", false)
	if !strings.Contains(dual, "listen [::]:80 default_server;") || !strings.Contains(dual, "listen [::]:443 ssl default_server;") {
		t.Fatal("dual-stack site must listen on IPv6", dual)
	}
	if strings.Contains(v4, "[::]") || !strings.Contains(v4, "listen 80 default_server;") || !strings.Contains(v4, "listen 443 ssl default_server;") {
		t.Fatal("IPv4-only site must omit IPv6 listeners", v4)
	}
	for _, ipv6 := range []bool{true, false} {
		if !m.ownedTLSSite([]byte(m.tlsSiteFor(c, false, "", ipv6)), c) {
			t.Fatal("generated site not recognized after IPv6 toggle", ipv6)
		}
	}
	if m.ownedTLSSite([]byte(dual), c) {
		t.Fatal("HTTPS site accepted as provisioning site")
	}
}
