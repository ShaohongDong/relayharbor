package manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/native"
)

func anyTLSFixture(t *testing.T) (*Manager, *State, *AnyTLSState, *[]string) {
	t.Helper()
	m, tc := tlsFixture(t)
	enableFixture(t, m, tc)
	content := []byte("verified sing-box fixture")
	hash := hashText(content)
	r := Release{SingBoxVersion, runtime.GOARCH, filepath.Join(filepath.Dir(m.P.Releases), "sing-box", SingBoxVersion, runtime.GOARCH, hash, "sing-box"), hash}
	if e := Atomic(r.Binary, content, 0755, -1); e != nil {
		t.Fatal(e)
	}
	s := &AnyTLSState{Schema: 1, Revision: "initial", Enabled: true, Domain: tc.Domain, Release: r}
	writeJSON(m.anyTLSPath(), s, 0600, -1)
	core := &State{Runtime: native.Options{Gateway: "127.0.0.1:45678", Auth: config.Auth{Username: "internal", Password: "internal-secret"}}}
	if e := writeJSON(m.P.Manifest, config.Initial("127.0.0.1:1080", false), 0600, -1); e != nil {
		t.Fatal(e)
	}
	calls := []string{}
	active, enabled := map[string]bool{}, map[string]bool{}
	m.AnyTLSProbe = func(context.Context, string, int) error { return nil }
	base := m.Exec
	m.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" || len(args) < 2 || !strings.Contains(strings.Join(args, " "), "gost-deploy-anytls-") {
			if name == r.Binary {
				return nil, nil
			}
			return base(ctx, name, args...)
		}
		calls = append(calls, strings.Join(args, " "))
		unit := args[len(args)-1]
		if args[0] == "show" {
			unit = args[1]
			switch args[3] {
			case "LoadState":
				return []byte("not-found"), nil
			case "FragmentPath":
				return []byte(filepath.Join(filepath.Dir(m.P.Unit), unit)), nil
			case "DropInPaths":
				return nil, nil
			}
		}
		switch args[0] {
		case "start", "restart":
			active[unit] = true
		case "stop":
			active[unit] = false
		case "enable":
			enabled[unit] = true
		case "disable":
			enabled[unit] = false
			if len(args) > 2 && args[1] == "--now" {
				active[unit] = false
			}
		case "is-active":
			if !active[unit] {
				return nil, errors.New("inactive")
			}
		case "is-enabled":
			if !enabled[unit] {
				return nil, errors.New("disabled")
			}
		}
		return nil, nil
	}
	return m, core, s, &calls
}

func TestAnyTLSUserIsolationAndRevocation(t *testing.T) {
	m, core, s, calls := anyTLSFixture(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: id, Name: "device-" + id}); e != nil {
			t.Fatal(e)
		}
	}
	if s.Users[0].Port == s.Users[1].Port || s.Users[0].Password == s.Users[1].Password {
		t.Fatal("not independent")
	}
	old := s.Users[0]
	beforeRevision := s.Revision
	*calls = nil
	if e := m.changeAnyTLS(ctx, core, s, "reset-token", AnyTLSRequest{ID: "a"}); e != nil {
		t.Fatal(e)
	}
	if len(*calls) != 0 || s.Revision == beforeRevision || s.Users[0].Password != old.Password {
		t.Fatal("token rotation touched instances")
	}
	if _, e := m.Subscription(ctx, old.Token, false); !errors.Is(e, ErrSubscription) {
		t.Fatal("old link accepted", e)
	}
	if b, e := m.Subscription(ctx, s.Users[0].Token, false); e != nil || !strings.Contains(string(b), old.Password) {
		t.Fatal("new link", e)
	}
	for _, op := range []string{"reset-password", "user-disable", "user-enable", "rename", "delete"} {
		*calls = nil
		if e := m.changeAnyTLS(ctx, core, s, op, AnyTLSRequest{ID: "a", Name: "new name"}); e != nil {
			t.Fatal(op, e)
		}
		for _, call := range *calls {
			if strings.Contains(call, anyTLSUnit("b")) {
				t.Fatal("other user touched", op, call)
			}
		}
		if op == "user-disable" {
			if _, e := m.Subscription(ctx, s.Users[0].Token, false); !errors.Is(e, ErrSubscription) {
				t.Fatal("disabled subscription accepted")
			}
		}
	}
	if e := m.guardAnyTLSTLS(""); e == nil {
		t.Fatal("TLS disable accepted")
	}
	if e := m.guardAnyTLSTLS("other.example.com"); e == nil {
		t.Fatal("TLS domain change accepted")
	}
	if e := m.changeAnyTLS(ctx, core, s, "disable", AnyTLSRequest{}); e != nil {
		t.Fatal(e)
	}
	if e := m.guardAnyTLSTLS(""); e != nil {
		t.Fatal(e)
	}
}

func TestAnyTLSRollbackAndResourceOwnership(t *testing.T) {
	m, core, s, _ := anyTLSFixture(t)
	ctx := context.Background()
	for _, port := range []int{1080, 18080, 45678} {
		if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: "reserved", Name: "reserved", Port: port}); e == nil {
			t.Fatal("accepted stopped managed listener port", port)
		}
	}
	if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: "a", Name: "one"}); e != nil {
		t.Fatal(e)
	}
	old := config.Bytes(s)
	oldConfig, _ := os.ReadFile(filepath.Join(m.anyTLSDir("a"), "config.json"))
	m.AnyTLSProbe = func(context.Context, string, int) error { return errors.New("injected readiness failure") }
	if e := m.changeAnyTLS(ctx, core, s, "reset-password", AnyTLSRequest{ID: "a"}); e == nil {
		t.Fatal("failure ignored")
	}
	got, _ := os.ReadFile(m.anyTLSPath())
	gotConfig, _ := os.ReadFile(filepath.Join(m.anyTLSDir("a"), "config.json"))
	if string(got) != string(old) || string(gotConfig) != string(oldConfig) {
		t.Fatal("rollback lost state")
	}
	if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: "failed", Name: "failed"}); e == nil {
		t.Fatal("failure ignored")
	}
	if exists(m.anyTLSDir("failed")) || exists(m.anyTLSUnitPath("failed")) {
		t.Fatal("failed add left resources")
	}
	if e := m.verifyAnyTLS(ctx, s); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(m.anyTLSUnitPath("a"), []byte("foreign"), 0644)
	if e := m.verifyAnyTLS(ctx, s); e == nil {
		t.Fatal("foreign unit accepted")
	}
}

func TestAnyTLSCertificateRollback(t *testing.T) {
	m, core, s, _ := anyTLSFixture(t)
	ctx := context.Background()
	if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: "a", Name: "a"}); e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(filepath.Join(m.anyTLSDir("a"), "cert.pem"))
	tc, _ := m.loadTLS()
	candidateCert(t, m, tc, tc.Domain, time.Now().Add(72*time.Hour))
	m.AnyTLSProbe = func(context.Context, string, int) error { return errors.New("injected hot reload failure") }
	if e := m.deployTLS(ctx, tc); e == nil {
		t.Fatal("reload failure ignored")
	}
	after, _ := os.ReadFile(filepath.Join(m.anyTLSDir("a"), "cert.pem"))
	live, _ := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/fullchain.pem"))
	if string(after) != string(old) || string(live) != string(old) {
		t.Fatal("certificate rollback incomplete")
	}
}

func TestAnyTLSEnableOccupiedPortPreservesDisabledUser(t *testing.T) {
	m, core, s, calls := anyTLSFixture(t)
	ctx := context.Background()
	if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: "a", Name: "a"}); e != nil {
		t.Fatal(e)
	}
	if e := m.changeAnyTLS(ctx, core, s, "user-disable", AnyTLSRequest{ID: "a"}); e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", s.Users[0].Port))
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	rev := s.Revision
	*calls = nil
	if e = m.changeAnyTLS(ctx, core, s, "user-enable", AnyTLSRequest{ID: "a"}); e == nil {
		t.Fatal("occupied user port accepted")
	}
	if s.Users[0].Enabled || s.Revision != rev {
		t.Fatal("failed enable mutated user")
	}
	for _, call := range *calls {
		if strings.HasPrefix(call, "start ") || strings.HasPrefix(call, "restart ") {
			t.Fatal("started occupied listener")
		}
	}
}

func TestSingBoxVerifiedArchiveAndSymlinkCache(t *testing.T) {
	archive, _ := filepath.Abs("../../.cache/releases/sing-box-" + SingBoxVersion + "-linux-" + runtime.GOARCH + ".tar.gz")
	if _, e := os.Stat(archive); e != nil {
		t.Skip("make fetch required")
	}
	m := fixtureManager(t)
	r, e := m.prepareSingBox(context.Background(), archive)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(r.Binary)
	if e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "binary")
	os.WriteFile(target, b, 0755)
	os.Remove(r.Binary)
	os.Symlink(target, r.Binary)
	if _, e = m.prepareSingBox(context.Background(), archive); e == nil {
		t.Fatal("symlink cache accepted")
	}
}

func TestAnyTLSUserEgressRoute(t *testing.T) {
	m, core, s, calls := anyTLSFixture(t)
	ctx := context.Background()
	c := config.Initial("127.0.0.1:1080", false)
	c.Profiles = append(c.Profiles, config.Profile{Name: "office", Type: "pool", Nodes: []config.Node{{ID: "a", Type: "http", Addr: "proxy.example:8080"}}, Probe: &config.Probe{Type: "tcp", Addr: "example.com:443", Interval: 30, Timeout: 5}})
	c.Entries = []config.Entry{{Name: "branch", Addr: "127.0.0.1:8443", Users: []config.Auth{{Username: "b", Password: "branch-secret"}}, Profile: "office"}}
	if e := writeJSON(m.P.Manifest, c, 0600, -1); e != nil {
		t.Fatal(e)
	}
	if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: "a", Name: "a", Profile: "missing"}); e == nil {
		t.Fatal("unknown profile accepted")
	}
	for _, id := range []string{"a", "b"} {
		if e := m.changeAnyTLS(ctx, core, s, "add", AnyTLSRequest{ID: id, Name: id}); e != nil {
			t.Fatal(e)
		}
	}
	if s.Users[0].Port == 8443 {
		t.Fatal("entry port was allocated to AnyTLS")
	}
	gateway := func(id string) string {
		var v map[string]any
		b, e := os.ReadFile(filepath.Join(m.anyTLSDir(id), "config.json"))
		if e != nil || json.Unmarshal(b, &v) != nil {
			t.Fatal(e)
		}
		return v["outbounds"].([]any)[0].(map[string]any)["username"].(string)
	}
	if gateway("a") != "internal" {
		t.Fatal("unbound user must follow activeProfile")
	}
	*calls = nil
	if e := m.changeAnyTLS(ctx, core, s, "route", AnyTLSRequest{ID: "a", Profile: "office"}); e != nil {
		t.Fatal(e)
	}
	if s.Users[0].Profile != "office" || gateway("a") != config.RouteProfile("internal", "office") || gateway("b") != "internal" {
		t.Fatal("route not written", s.Users[0])
	}
	restarted := false
	for _, call := range *calls {
		if strings.Contains(call, anyTLSUnit("b")) {
			t.Fatal("other user touched", call)
		}
		restarted = restarted || call == "restart "+anyTLSUnit("a")
	}
	if !restarted {
		t.Fatal("bound user not restarted", *calls)
	}
	if e := m.changeAnyTLS(ctx, core, s, "route", AnyTLSRequest{ID: "a", Profile: "missing"}); e == nil || s.Users[0].Profile != "office" {
		t.Fatal("unknown profile route accepted")
	}
	removed := c.Clone()
	removed.Entries = nil
	removed.Profiles = removed.Profiles[:1]
	if e := m.anyTLSBindings(removed); e == nil || !strings.Contains(e.Error(), "a") {
		t.Fatal("removing a pinned profile was allowed", e)
	}
	if e := m.changeAnyTLS(ctx, core, s, "route", AnyTLSRequest{ID: "a"}); e != nil || gateway("a") != "internal" {
		t.Fatal("route reset", e)
	}
	if e := m.anyTLSBindings(removed); e != nil {
		t.Fatal(e)
	}
}
