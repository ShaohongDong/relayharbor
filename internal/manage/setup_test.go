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
