package manage

import (
	"context"
	"errors"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/native"
	"github.com/ShaohongDong/relayharbor/internal/supervisor"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixtureManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	p := func(s string) string { return filepath.Join(root, s) }
	m := New()
	m.P = Paths{p("etc/gost-deploy/config.json"), p("etc/gost/gost.json"), p("etc/gost/runtime.json"), p("var/lib/gost-deploy/state.json"), p("var/lib/gost-deploy/api-token"), p("etc/systemd/system/gost.service"), p("etc/systemd/system/gost-deploy-api.service"), p("usr/local/sbin/gostctl"), p("opt/gost-deploy/releases"), p("opt/gost-deploy/managers"), p("run/gost-deploy/control.sock"), p("run/lock/gost.lock")}
	m.Self = p("source-manager")
	os.WriteFile(m.Self, []byte("manager"), 0755)
	m.Group = func() (int, error) { return os.Getgid(), nil }
	return m
}
func hotFixture(t *testing.T) (*Manager, *State, config.Config) {
	t.Helper()
	m := fixtureManager(t)
	c := config.Initial("127.0.0.1:1080", false)
	c.Profiles = append(c.Profiles, config.Profile{Name: "second", Type: "direct"})
	hash, _ := Digest(m.Self)
	s := &State{Schema: 3, Installed: true, Current: Release{Version: "3.3.0", Binary: "gost"}, Revision: "before", Runtime: native.Options{Gateway: "127.0.0.1:20000", Auth: config.Auth{Username: "internal", Password: "secret"}}, Manager: m.Self, ManagerSHA: hash}
	if e := m.write(c, s.Current, s.Runtime, s.Revision); e != nil {
		t.Fatal(e)
	}
	writeJSON(m.P.State, s, 0600, -1)
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && (args[0] != "is-active" && args[0] != "is-enabled") {
			t.Fatalf("hot change called systemctl %v", args)
		}
		if name == "systemctl" && args[0] == "is-enabled" {
			return nil, errors.New("disabled")
		}
		return nil, nil
	}
	return m, s, c
}
func TestHotPublishAfterPersistence(t *testing.T) {
	m, s, c := hotFixture(t)
	calls := []string{}
	m.RPC = func(_ context.Context, req supervisor.Request, out any) error {
		calls = append(calls, req.Command)
		if req.Command == "commit" {
			state, e := m.Load(false)
			if e != nil || state.Revision != req.Revision {
				t.Fatal("published before persistence")
			}
			out.(*supervisor.Status).Revision = req.Revision
		}
		return nil
	}
	c.ActiveProfile = "second"
	if e := m.Apply(context.Background(), s, s.Current, c, nil, false); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(calls, []string{"prepare", "commit"}) {
		t.Fatal(calls)
	}
	state, _ := m.Load(false)
	if state.Previous.Config.ActiveProfile != "direct" {
		t.Fatal("rollback snapshot")
	}
}
func TestLostCommitAcknowledgementRecovery(t *testing.T) {
	m, s, c := hotFixture(t)
	before, _ := os.ReadFile(m.P.State)
	observed := "before"
	failed := false
	m.RPC = func(_ context.Context, req supervisor.Request, out any) error {
		switch req.Command {
		case "commit":
			observed = req.Revision
			if !failed {
				failed = true
				return errors.New("lost ack")
			}
		case "status":
			out.(*supervisor.Status).Revision = observed
		}
		return nil
	}
	c.ActiveProfile = "second"
	e := m.Apply(context.Background(), s, s.Current, c, nil, false)
	if e == nil || !strings.Contains(e.Error(), "已恢复") {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(m.P.State)
	if string(before) != string(after) || observed != "before" {
		t.Fatal("recovery diverged")
	}
	current, _ := m.ReadConfig()
	if current.ActiveProfile != "direct" {
		t.Fatal("disk config changed")
	}
}
func TestFreshFailureCleansNewResources(t *testing.T) {
	m := fixtureManager(t)
	created, removed := false, false
	m.Exec = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "getent" {
			return nil, errors.New("missing")
		}
		if name == "useradd" {
			created = true
		}
		if name == "userdel" {
			removed = true
		}
		if name == "systemctl" && len(args) > 1 && args[0] == "start" && args[1] == "gost.service" {
			return nil, errors.New("injected startup failure")
		}
		return nil, nil
	}
	e := m.Apply(context.Background(), nil, Release{Binary: "candidate"}, config.Initial("127.0.0.1:1080", false), nil, true)
	if e == nil || !strings.Contains(e.Error(), "已撤销") || !created || !removed {
		t.Fatalf("cleanup %v %v %v", e, created, removed)
	}
	for _, p := range []string{m.P.Unit, m.P.APIUnit, m.P.Control, m.P.Manifest, m.P.State, m.P.Managers} {
		if exists(p) {
			t.Fatalf("leftover %s", p)
		}
	}
}
func TestVerifyReleaseRejectsEscapeAndTamper(t *testing.T) {
	m := fixtureManager(t)
	path := filepath.Join(m.P.Releases, "3.3.0/amd64/gost")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("binary"), 0755)
	hash, _ := Digest(path)
	r := Release{Binary: path, SHA256: hash}
	if e := m.VerifyRelease(r); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(path, []byte("tampered"), 0755)
	if m.VerifyRelease(r) == nil {
		t.Fatal("tampering allowed")
	}
	outside := m.Self
	hash, _ = Digest(outside)
	if m.VerifyRelease(Release{Binary: outside, SHA256: hash}) == nil {
		t.Fatal("escape allowed")
	}
}
func TestUnsupportedStateRejected(t *testing.T) {
	m := fixtureManager(t)
	for _, schema := range []int{1, 2, 4} {
		writeJSON(m.P.State, map[string]int{"schema": schema}, 0600, -1)
		if _, e := m.Load(false); e == nil {
			t.Fatal("old state accepted")
		}
	}
}
func TestAtomicPermissionsAndNoSymlinkFollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte("foreign"), 0600)
	os.Symlink(other, path)
	if e := Atomic(path, []byte("new"), 0600, -1); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(other)
	st, _ := os.Stat(path)
	if string(b) != "foreign" || st.Mode().Perm() != 0600 {
		t.Fatal("unsafe atomic write")
	}
}
