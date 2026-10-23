package supervisor

import (
	"context"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/native"
	"github.com/ShaohongDong/relayharbor/internal/socks"
	"github.com/ShaohongDong/relayharbor/internal/testkit"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

func TestRealRouting(t *testing.T) {
	binary := testkit.GOST(t)
	echo := testkit.Echo(t)
	udp := testkit.UDP(t)
	auth := config.Auth{Username: "upstream", Password: "test-upstream-password"}
	nodes := []config.Node{}
	upstreams := []*exec.Cmd{}
	for _, kind := range []string{"http", "socks5"} {
		addr := testkit.Addr(t)
		v := native.Service(addr, []config.Auth{auth}, false)
		v["services"].([]any)[0].(native.M)["handler"].(native.M)["type"] = kind
		upstreams = append(upstreams, testkit.Start(t, binary, v, addr))
		nodes = append(nodes, config.Node{ID: kind, Type: kind, Addr: addr, Auth: &auth})
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unavailable" {
			w.WriteHeader(503)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	publicAddr := testkit.Addr(t)
	public := native.M{"services": []any{native.M{"name": "public-http", "addr": publicAddr, "handler": native.M{"type": "http"}, "listener": native.M{"type": "tcp"}}}}
	testkit.Start(t, binary, public, publicAddr)
	cfg := config.Initial(testkit.Addr(t), true)
	cfg.Profiles = append(cfg.Profiles, config.Profile{Name: "mixed", Type: "pool", Nodes: nodes, Probe: &config.Probe{Type: "tcp", Addr: echo, Interval: 1, Timeout: 1}}, config.Profile{Name: "public", Type: "pool", Nodes: []config.Node{{ID: "public", Type: "http", Addr: publicAddr}}, Probe: &config.Probe{Type: "http", URL: target.URL + "/ready", ExpectedStatus: 200, Interval: 30, Timeout: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	r := New(Input{cfg, native.Options{Gateway: testkit.Addr(t), Auth: config.Auth{Username: "internal", Password: "test-internal-password"}}, binary, "initial"}, t.TempDir())
	r.Command = testkit.Command
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if e := <-done; e != nil && e != context.Canceled {
			t.Error(e)
		}
	})
	testkit.Wait(t, 8*time.Second, func() bool { s := r.Status(); return s.Ready && s.FrontendPID > 0 && testkit.Ready(cfg) })
	pid := r.Status().FrontendPID
	switchTo := func(name string) {
		t.Helper()
		cfg.ActiveProfile = name
		rev := config.Secret()
		if e := r.Prepare(cfg, rev); e != nil {
			t.Fatal(e)
		}
		s, e := r.Commit(rev)
		if e != nil || s.Revision != rev || s.ActiveProfile != name || s.FrontendPID != pid {
			t.Fatalf("switch ack: %+v %v", s, e)
		}
	}
	_, targetPort, _ := net.SplitHostPort(echo)
	domainTarget := net.JoinHostPort("localhost", targetPort)
	direct := testkit.Connect(t, cfg, domainTarget)
	defer direct.Close()
	if e := testkit.Payload(direct); e != nil {
		t.Fatal(e)
	}
	testkit.CheckAuth(t, cfg)
	t.Log("PASS direct TCP/domain and mandatory authentication")
	if e := testkit.UDPRelay(cfg, udp, true); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS direct UDP through internal GOST tunnel")
	switchTo("mixed")
	pool := testkit.Connect(t, cfg, domainTarget)
	defer pool.Close()
	if e := testkit.Payload(pool); e != nil {
		t.Fatal(e)
	}
	rotated := []net.Conn{}
	for i := 0; i < 4; i++ {
		c := testkit.Connect(t, cfg, echo)
		if e := testkit.Payload(c); e != nil {
			t.Fatal(e)
		}
		rotated = append(rotated, c)
	}
	r.mu.Lock()
	httpConnections := r.current.workers[key("mixed", "http")].refs.Load()
	socksConnections := r.current.workers[key("mixed", "socks5")].refs.Load()
	r.mu.Unlock()
	if httpConnections < 2 || socksConnections < 2 {
		t.Fatalf("traffic did not reach both upstreams: %d/%d", httpConnections, socksConnections)
	}
	for _, c := range rotated {
		c.Close()
	}
	t.Log("PASS mixed authenticated HTTP/SOCKS5 round robin and acknowledged switch")
	switchTo("direct")
	if e := testkit.Payload(direct); e != nil {
		t.Fatal(e)
	}
	if e := testkit.Payload(pool); e != nil {
		t.Fatal(e)
	}
	switchTo("mixed")
	if e := testkit.UDPRelay(cfg, udp, false); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS existing direct/pool streams survive switches; pool UDP denied")
	_ = upstreams[0].Process.Kill()
	r.mu.Lock()
	r.current.rounds["mixed"] = 0
	r.mu.Unlock()
	c := testkit.Connect(t, cfg, echo)
	if e := testkit.Payload(c); e != nil {
		t.Fatal(e)
	}
	c.Close()
	_ = upstreams[1].Process.Kill()
	ctx2, cancel2 := context.WithTimeout(ctx, 8*time.Second)
	_, _, e := socks.Dial(ctx2, cfg.Listener.Addr, cfg.Listener.Users[0], socks.Connect, socks.Target(echo))
	cancel2()
	if e == nil {
		t.Fatal("all-dead pool allowed forwarding")
	}
	t.Log("PASS request failover and all-dead pool fails closed")
	// Recreate both upstream listeners; recovery must wait for the cooldown.
	for i, n := range nodes {
		v := native.Service(n.Addr, []config.Auth{auth}, false)
		v["services"].([]any)[0].(native.M)["handler"].(native.M)["type"] = n.Type
		upstreams[i] = testkit.Start(t, binary, v, n.Addr)
	}
	if r.Status().Available {
		t.Fatal("cooldown bypassed")
	}
	testkit.Wait(t, 16*time.Second, func() bool { return r.Status().HealthyNodes == 2 })
	c = testkit.Connect(t, cfg, echo)
	c.Close()
	t.Log("PASS cooldown and periodic end-to-end recovery")
	before := r.Status().Revision
	bad := cfg.Clone()
	bad.ActiveProfile = "missing"
	if r.Prepare(bad, "bad") == nil || r.Status().Revision != before {
		t.Fatal("invalid prepare changed routing")
	}
	r.mu.Lock()
	w := r.current.workers[key("mixed", "http")]
	r.mu.Unlock()
	oldPID := w.pid()
	w.mu.Lock()
	_ = w.cmd.Process.Kill()
	w.mu.Unlock()
	testkit.Wait(t, 5*time.Second, func() bool { return w.pid() != oldPID })
	t.Log("PASS invalid prepare preservation and worker supervision")
	switchTo("public")
	c = testkit.Connect(t, cfg, echo)
	c.Close()
	cfg.Profiles[2].Probe.URL = target.URL + "/unavailable"
	switchTo("public")
	if r.Status().Available {
		t.Fatal("HTTP 503 probe was accepted")
	}
	t.Log("PASS unauthenticated HTTP upstream and response-status rejection")
	switchTo("mixed")
	held := testkit.Connect(t, cfg, echo)
	if e := testkit.Payload(held); e != nil {
		t.Fatal(e)
	}
	r.mu.Lock()
	g := r.current
	var retained *Worker
	for _, worker := range g.workers {
		if worker.refs.Load() > 0 && worker.node != nil {
			retained = worker
			break
		}
	}
	r.mu.Unlock()
	if retained == nil {
		t.Fatal("no retained worker")
	}
	switchTo("direct")
	cfg.Profiles = []config.Profile{cfg.Profiles[0]}
	switchTo("direct")
	if e := testkit.Payload(held); e != nil {
		t.Fatal(e)
	}
	held.Close()
	pool.Close()
	testkit.Wait(t, 5*time.Second, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.workers[retained.key] == nil })
	t.Log("PASS removed workers drain existing streams before exit")
}

func TestRealRoutingIPv6(t *testing.T) {
	binary := testkit.GOST(t)
	if l, e := net.Listen("tcp", "[::1]:0"); e != nil {
		t.Skip("IPv6 loopback unavailable:", e)
	} else {
		l.Close()
	}
	echo := testkit.EchoOn(t, "::1")
	auth := config.Auth{Username: "upstream", Password: "test-upstream-password"}
	upstream := testkit.AddrOn(t, "::1")
	testkit.Start(t, binary, native.Service(upstream, []config.Auth{auth}, false), upstream)
	cfg := config.Initial(testkit.AddrOn(t, "::1"), false)
	cfg.Profiles = append(cfg.Profiles, config.Profile{Name: "v6", Type: "pool", Nodes: []config.Node{{ID: "v6", Type: "socks5", Addr: upstream, Auth: &auth}}, Probe: &config.Probe{Type: "tcp", Addr: echo, Interval: 1, Timeout: 1}})
	if e := cfg.Validate(false); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := New(Input{cfg, native.Options{Gateway: testkit.Addr(t), Auth: config.Auth{Username: "internal", Password: "test-internal-password"}}, binary, "initial"}, t.TempDir())
	r.Command = testkit.Command
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if e := <-done; e != nil && e != context.Canceled {
			t.Error(e)
		}
	})
	testkit.Wait(t, 8*time.Second, func() bool { s := r.Status(); return s.Ready && testkit.Ready(cfg) })
	direct := testkit.Connect(t, cfg, echo)
	defer direct.Close()
	if e := testkit.Payload(direct); e != nil {
		t.Fatal(e)
	}
	testkit.CheckAuth(t, cfg)
	t.Log("PASS IPv6 listener and direct IPv6 target")
	cfg.ActiveProfile = "v6"
	rev := config.Secret()
	if e := r.Prepare(cfg, rev); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Commit(rev); e != nil {
		t.Fatal(e)
	}
	testkit.Wait(t, 8*time.Second, func() bool { return r.Status().HealthyNodes == 1 })
	pool := testkit.Connect(t, cfg, echo)
	defer pool.Close()
	if e := testkit.Payload(pool); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS IPv6 SOCKS5 upstream, IPv6 probe and IPv6 target")
}

func TestRealEgressRoutes(t *testing.T) {
	binary := testkit.GOST(t)
	echo := testkit.Echo(t)
	auth := config.Auth{Username: "upstream", Password: "test-upstream-password"}
	upstream := testkit.Addr(t)
	testkit.Start(t, binary, native.Service(upstream, []config.Auth{auth}, false), upstream)
	probe := &config.Probe{Type: "tcp", Addr: echo, Interval: 1, Timeout: 1}
	cfg := config.Initial(testkit.Addr(t), false)
	cfg.Profiles = append(cfg.Profiles,
		config.Profile{Name: "live", Type: "pool", Nodes: []config.Node{{ID: "live", Type: "socks5", Addr: upstream, Auth: &auth}}, Probe: probe},
		config.Profile{Name: "dead", Type: "pool", Nodes: []config.Node{{ID: "dead", Type: "socks5", Addr: testkit.Addr(t), Auth: &auth}}, Probe: probe})
	entry := config.Entry{Name: "office", Addr: testkit.Addr(t), Users: []config.Auth{{Username: "office", Password: "test-office-password"}}, Profile: "dead"}
	cfg.Entries = []config.Entry{entry}
	if e := cfg.Validate(false); e != nil {
		t.Fatal(e)
	}
	gateway := native.Options{Gateway: testkit.Addr(t), Auth: config.Auth{Username: "internal", Password: "test-internal-password"}}
	ctx, cancel := context.WithCancel(context.Background())
	r := New(Input{cfg, gateway, binary, "initial"}, t.TempDir())
	r.Command = testkit.Command
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if e := <-done; e != nil && e != context.Canceled {
			t.Error(e)
		}
	})
	testkit.Wait(t, 8*time.Second, func() bool {
		s := r.Status()
		return s.Ready && testkit.Ready(cfg) && len(s.Entries) == 1 && s.Entries[0].PID > 0 && s.HealthyNodes == 0
	})
	dial := func(addr string, a config.Auth) (net.Conn, error) {
		dctx, stop := context.WithTimeout(ctx, 8*time.Second)
		defer stop()
		conn, _, err := socks.Dial(dctx, testkit.Local(addr), a, socks.Connect, socks.Target(echo))
		return conn, err
	}
	direct := testkit.Connect(t, cfg, echo)
	defer direct.Close()
	if e := testkit.Payload(direct); e != nil {
		t.Fatal(e)
	}
	if c, e := dial(entry.Addr, entry.Users[0]); e == nil {
		c.Close()
		t.Fatal("entry bound to a dead pool forwarded traffic")
	}
	if c, e := dial(entry.Addr, cfg.Listener.Users[0]); e == nil {
		c.Close()
		t.Fatal("entry accepted a shared listener user")
	}
	t.Log("PASS shared listener direct while entry fails closed on its own dead pool")
	pinned := gateway.Auth
	pinned.Username = config.RouteProfile(gateway.Auth.Username, "live")
	testkit.Wait(t, 8*time.Second, func() bool { s := r.Status(); return s.Nodes[0].Healthy != nil && *s.Nodes[0].Healthy })
	c, e := dial(gateway.Gateway, pinned)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	for _, bad := range []string{config.RouteProfile(gateway.Auth.Username, "missing"), config.RouteEntry(gateway.Auth.Username, "missing"), "internal:other"} {
		a := gateway.Auth
		a.Username = bad
		if c, e := dial(gateway.Gateway, a); e == nil {
			c.Close()
			t.Fatalf("gateway accepted route %q", bad)
		}
	}
	t.Log("PASS pinned profile route and unknown routes rejected")
	pid := r.Status().Entries[0].PID
	held := testkit.Connect(t, cfg, echo)
	defer held.Close()
	cfg.Entries[0].Profile = "live"
	rev := config.Secret()
	if e := r.Prepare(cfg, rev); e != nil {
		t.Fatal(e)
	}
	s, e := r.Commit(rev)
	if e != nil || s.Entries[0].Profile != "live" || !s.Entries[0].Available || s.Entries[0].PID != pid || s.ActiveProfile != "direct" {
		t.Fatalf("entry rebind ack: %+v %v", s, e)
	}
	c, e = dial(entry.Addr, entry.Users[0])
	if e != nil {
		t.Fatal(e)
	}
	if e := testkit.Payload(c); e != nil {
		t.Fatal(e)
	}
	c.Close()
	if e := testkit.Payload(held); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS entry rebinding is hot and keeps entry PID and existing streams")
	moved := cfg.Clone()
	moved.Entries[0].Addr = testkit.Addr(t)
	if r.Prepare(moved, "moved") == nil {
		t.Fatal("entry address change was hot-applied")
	}
	t.Log("PASS entry address change requires restart")
}
