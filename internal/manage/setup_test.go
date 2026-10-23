package manage

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccessCertificateAndHandoff(t *testing.T) {
	m := fixtureManager(t)
	if e := os.MkdirAll(filepath.Dir(m.P.Lock), 0700); e != nil {
		t.Fatal(e)
	}
	v := AccessConfig{Host: "192.0.2.5", Port: 18443}
	if e := writeJSON(m.accessPath(), v, 0600, -1); e != nil {
		t.Fatal(e)
	}
	one, e := m.AccessCertificate()
	if e != nil {
		t.Fatal(e)
	}
	two, e := m.AccessCertificate()
	if e != nil || string(one.Certificate[0]) != string(two.Certificate[0]) {
		t.Fatal("certificate not stable", e)
	}
	leaf, e := x509.ParseCertificate(one.Certificate[0])
	if e != nil || leaf.VerifyHostname(v.Host) != nil {
		t.Fatal("hostname")
	}
	if e = m.AccessHandoff(); e != nil {
		t.Fatal(e)
	}
	a, _ := m.Access()
	if a.CloseAt == nil || time.Until(*a.CloseAt) < 4*time.Minute {
		t.Fatal("handoff deadline")
	}
	if e = m.AccessHandoff(); e != nil {
		t.Fatal(e)
	}
	b, _ := m.Access()
	if !a.CloseAt.Equal(*b.CloseAt) {
		t.Fatal("deadline extended on retry")
	}
	if err := validateAccess(AccessConfig{Host: "bad/name", Port: 18443}); err == nil {
		t.Fatal("invalid host")
	}
}
func TestAccessIPv6Literal(t *testing.T) {
	for in, want := range map[string]string{"[2001:DB8:0::1]": "2001:db8::1", "2001:db8::1": "2001:db8::1", "192.0.2.5": "192.0.2.5", "panel.example.com": "panel.example.com"} {
		if got := canonicalHost(in); got != want {
			t.Fatalf("canonicalHost(%q) = %q", in, got)
		}
	}
	for _, host := range []string{"2001:DB8::1", "fe80::1%eth0", "[2001:db8::1]"} {
		if validateAccess(AccessConfig{Host: host, Port: 18443}) == nil {
			t.Fatal("non-canonical or scoped IPv6 accepted", host)
		}
	}
	m := fixtureManager(t)
	if e := os.MkdirAll(filepath.Dir(m.P.Lock), 0700); e != nil {
		t.Fatal(e)
	}
	// Records written by older releases are canonicalized on read.
	if e := writeJSON(m.accessPath(), AccessConfig{Host: "2001:DB8:0::1", Port: 18443}, 0600, -1); e != nil {
		t.Fatal(e)
	}
	v, e := m.Access()
	if e != nil || v.Host != "2001:db8::1" || v.URL() != "https://[2001:db8::1]:18443" {
		t.Fatal("stored IPv6 host", v, e)
	}
	cert, e := m.AccessCertificate()
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil || leaf.VerifyHostname("2001:db8::1") != nil {
		t.Fatal("IPv6 certificate SAN", e)
	}
	if m.AccessOrigin("[2001:db8::1]:18443", true) != "https://[2001:db8::1]:18443" {
		t.Fatal("IPv6 Host header not accepted as access origin")
	}
}
func TestInterruptedJobAndOwnership(t *testing.T) {
	m := fixtureManager(t)
	j := SetupJob{ID: "0123456789abcdef0123456789abcdef", Kind: "tls", Status: "running", Started: time.Now().Add(-time.Minute)}
	if e := m.saveJob(j); e != nil {
		t.Fatal(e)
	}
	if e := Atomic(filepath.Join(m.jobsDir(), "latest"), []byte(j.ID), 0600, -1); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(m.SetupBusy(), ErrSetupBusy) {
		t.Fatal("active job not reserved")
	}
	m.SetupJob = j.ID
	if e := m.SetupBusy(); e != nil {
		t.Fatal("worker blocked itself")
	}
	m.SetupJob = ""
	m.Exec = func(context.Context, string, ...string) ([]byte, error) { return []byte("inactive\n"), nil }
	if e := m.ReconcileSetup(context.Background()); e != nil {
		t.Fatal(e)
	}
	got, e := m.ReadSetupJob(j.ID)
	if e != nil || got.Status != "interrupted" || got.Finished == nil {
		t.Fatal(got, e)
	}
	if e = m.SetupBusy(); e != nil {
		t.Fatal("interrupted job blocks retry")
	}
	if _, e = m.ReadSetupJob("../../auth"); e == nil {
		t.Fatal("path traversal")
	}
}
func TestSetupIdempotency(t *testing.T) {
	m := fixtureManager(t)
	// An existing idempotent submission must not call systemd or change status,
	// even if later configuration revisions differ.
	key := "a-valid-idempotency-key"
	_, e := m.StartSetup(context.Background(), "anytls", key, "old", TLSOptions{})
	if e == nil {
		t.Fatal("missing core accepted")
	}
	// Invalid requests fail before touching any core or system resources.
	if _, e = m.StartSetup(context.Background(), "tls", key, "old", TLSOptions{Domain: "example.com"}); e == nil {
		t.Fatal("missing TOS accepted")
	}
	if _, e = m.StartSetup(context.Background(), "anytls", "short", "old", TLSOptions{}); e == nil {
		t.Fatal("short key accepted")
	}
}
func TestSetupClosedAfterCompletion(t *testing.T) {
	m, _, _ := hotFixture(t)
	if e := os.MkdirAll(filepath.Dir(m.P.Lock), 0700); e != nil {
		t.Fatal(e)
	}
	if e := writeJSON(m.accessPath(), AccessConfig{Host: "192.0.2.5", Port: 18443}, 0600, -1); e != nil {
		t.Fatal(e)
	}
	if e := m.SetupComplete("before"); e != nil {
		t.Fatal(e)
	}
	if e := m.SetupComplete("before"); !errors.Is(e, ErrSetupCompleted) {
		t.Fatal("completed twice", e)
	}
	// Completion is checked before any service is managed or a job is started.
	o := TLSOptions{Domain: "example.com", Email: "admin@example.com", AcceptTOS: true}
	if _, e := m.StartSetup(context.Background(), "tls", "a-valid-idempotency-key", "before", o); !errors.Is(e, ErrSetupCompleted) {
		t.Fatal("tls setup after completion", e)
	}
	if e := writeJSON(m.anyTLSPath(), AnyTLSState{Schema: 1, Revision: "r1", Domain: "example.com"}, 0600, -1); e != nil {
		t.Fatal(e)
	}
	if _, e := m.StartSetup(context.Background(), "anytls", "another-idempotency-key", "before", TLSOptions{}); !errors.Is(e, ErrAnyTLSConfigured) {
		t.Fatal("anytls initialized twice", e)
	}
}
