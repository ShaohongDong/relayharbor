package integration

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/api"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"github.com/ShaohongDong/relayharbor/internal/native"
	"github.com/ShaohongDong/relayharbor/internal/testkit"
)

// Shares the isolated ACME deployment so renewal exercises Nginx and both
// independent AnyTLS instances with the same real certificate.
func anyTLSAcceptance(t *testing.T, m *manage.Manager, c config.Config, client *http.Client) func() {
	t.Helper()
	t.Setenv("GOST_ARCHIVE", "/artifacts/gost_3.3.0_linux_amd64.tar.gz")
	ctx := context.Background()
	tokenPath := "/var/lib/gost-deploy/api-token"
	run(t, "anytls", "setup", "--sing-box-archive", "/artifacts/sing-box-1.14.3-linux-amd64.tar.gz")
	call := func(op string, body any) json.RawMessage {
		t.Helper()
		_, rev, e := api.Client(ctx, tokenPath, "GET", "/anytls", nil, "")
		if e != nil {
			t.Fatal(e)
		}
		data, _, e := api.Client(ctx, tokenPath, "POST", "/anytls/"+op, body, rev)
		if e != nil {
			b, _ := exec.Command("journalctl", "-u", "gost-deploy-anytls-*", "-n", "20", "--no-pager").CombinedOutput()
			t.Log(string(b))
			t.Fatal(op, e)
		}
		return data
	}
	for _, id := range []string{"phone", "laptop"} {
		call("users", map[string]any{"id": id, "name": id})
	}
	credentials := func(id string) manage.AnyTLSCredentials {
		var v manage.AnyTLSCredentials
		if e := json.Unmarshal(call("users/"+id+"/credentials", map[string]any{}), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	fetch := func(url string, want int) []byte {
		t.Helper()
		r, e := client.Get(url)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		b, e := io.ReadAll(r.Body)
		if e != nil || r.StatusCode != want {
			t.Fatalf("subscription HTTP %d want %d: %v", r.StatusCode, want, e)
		}
		if want == 200 && r.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("subscription cache enabled")
		}
		return b
	}
	a, b := credentials("phone"), credentials("laptop")
	mihomo := func(v manage.AnyTLSCredentials) string {
		return testkit.Mihomo(t, "/artifacts/mihomo-linux-amd64-compatible-v1.19.32.gz", fetch(v.URL, 200), "/var/lib/gost-deploy/pebble-serving-root.pem")
	}
	addrA, addrB := mihomo(a), mihomo(b)
	echo := testkit.Echo(t)
	connect := func(addr string) net.Conn {
		t.Helper()
		conn, e := testkit.ClientConnect(addr, echo)
		if e != nil {
			t.Fatal("Mihomo -> AnyTLS -> GOST", e)
		}
		if e = testkit.Payload(conn); e != nil {
			conn.Close()
			t.Fatal("AnyTLS payload", e)
		}
		return conn
	}
	heldA, heldB := connect(addrA), connect(addrB)
	t.Cleanup(func() { heldA.Close(); heldB.Close() })
	pidB := pid(t, "gost-deploy-anytls-laptop")
	usage, e := exec.Command("ps", "-o", "pid,rss,comm", "-p", pidB+","+pid(t, "gost-deploy-anytls-phone")).Output()
	if e != nil {
		t.Fatal(e)
	}
	t.Log("AnyTLS two-user RSS snapshot (KiB):\n" + string(usage))
	assertB := func() {
		t.Helper()
		if pid(t, "gost-deploy-anytls-laptop") != pidB || testkit.Payload(heldB) != nil {
			t.Fatal("other user interrupted")
		}
	}
	call("users/phone/reset-token", map[string]any{})
	fetch(a.URL, 404)
	if e := testkit.Payload(heldA); e != nil {
		t.Fatal("token rotation interrupted proxy", e)
	}
	assertB()
	a = credentials("phone")
	fetch(a.URL, 200)
	fetch(a.Provider, 200)
	fetch(strings.Replace(a.URL, "https://", "http://", 1), 404)
	call("users/phone/reset-password", map[string]any{})
	heldA.SetDeadline(time.Now().Add(2 * time.Second))
	if e := testkit.Payload(heldA); e == nil {
		t.Fatal("old authenticated connection survived password revocation")
	}
	assertB()
	// A client with the old password must not establish a working tunnel.
	bad, e := testkit.ClientConnect(addrA, echo)
	if e == nil {
		defer bad.Close()
		if testkit.Payload(bad) == nil {
			t.Fatal("old password accepted")
		}
	}
	a = credentials("phone")
	addrNew := mihomo(a)
	renewed := connect(addrNew)
	renewed.Close()
	call("users/phone/disable", map[string]any{})
	fetch(a.URL, 404)
	assertB()
	call("users/phone/enable", map[string]any{})
	assertB()
	call("users/phone/delete", map[string]any{})
	fetch(a.URL, 404)
	assertB()
	if e = m.TLSDisable(ctx, false); e == nil {
		t.Fatal("HTTPS disable allowed while AnyTLS enabled")
	}
	// The actual client must follow hot profile changes and pool fail-closed policy.
	auth := config.Auth{Username: "upstream", Password: "test-upstream-secret"}
	nodes := []config.Node{}
	upstreams := []*exec.Cmd{}
	binary := testkit.GOST(t)
	for _, kind := range []string{"http", "socks5"} {
		addr := testkit.Addr(t)
		v := native.Service(addr, []config.Auth{auth}, false)
		v["services"].([]any)[0].(native.M)["handler"].(native.M)["type"] = kind
		upstreams = append(upstreams, testkit.Start(t, binary, v, addr))
		nodes = append(nodes, config.Node{ID: kind, Type: kind, Addr: addr, Auth: &auth})
	}
	pool := c.Clone()
	pool.Profiles = append(pool.Profiles, config.Profile{Name: "anytls-pool", Type: "pool", Nodes: nodes, Probe: &config.Probe{Type: "tcp", Addr: echo, Interval: 1, Timeout: 1}})
	pool.ActiveProfile = "anytls-pool"
	apply := func(v config.Config) {
		_, rev, e := api.Client(ctx, tokenPath, "GET", "/config", nil, "")
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = api.Client(ctx, tokenPath, "PUT", "/config", v, rev); e != nil {
			t.Fatal(e)
		}
	}
	apply(pool)
	assertB()
	for i := 0; i < 4; i++ {
		conn := connect(addrB)
		conn.Close()
	}
	upstreams[0].Process.Kill()
	for i := 0; i < 3; i++ {
		conn := connect(addrB)
		conn.Close()
	}
	upstreams[1].Process.Kill()
	testkit.Wait(t, 8*time.Second, func() bool {
		conn, e := testkit.ClientConnect(addrB, echo)
		if e != nil {
			return true
		}
		defer conn.Close()
		return testkit.Payload(conn) != nil
	})
	apply(c)
	assertB()
	conn := connect(addrB)
	conn.Close()
	t.Log("PASS AnyTLS Mihomo subscription + user isolation + token/password revocation + pool failover/fail-closed")
	// Called immediately after the existing due-renewal test.
	return func() {
		assertB()
		conn := connect(addrB)
		conn.Close()
		// The production renewal hook already pins the actually served leaf on
		// each active instance; this adds an end-to-end request and held stream.
		t.Log("PASS AnyTLS certificate hot reload + held Mihomo stream + unchanged instance PID")
		// Keep the remaining user for the reboot acceptance, but disable it
		// temporarily so the existing TLS-disable/re-enable test remains valid.
		call("disable", map[string]any{})
	}
}

func verifyAnyTLSBoot(t *testing.T) {
	t.Helper()
	m := manage.New()
	data, _, e := m.AnyTLS(context.Background(), "status", manage.AnyTLSRequest{}, "")
	if e != nil {
		t.Fatal(e)
	}
	status := data.(manage.AnyTLSStatus)
	if !status.Enabled || len(status.Users) != 1 || !status.Users[0].Active {
		t.Fatal("AnyTLS reboot recovery", status)
	}
	t.Log("PASS AnyTLS independent systemd instance reboot recovery")
}
