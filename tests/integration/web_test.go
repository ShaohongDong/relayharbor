package integration

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/api"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"github.com/ShaohongDong/relayharbor/internal/native"
	"github.com/ShaohongDong/relayharbor/internal/supervisor"
	"github.com/ShaohongDong/relayharbor/internal/testkit"
)

func apiCall(t *testing.T, method, path string, body any, rev string) (json.RawMessage, string) {
	t.Helper()
	b, r, e := api.Client(context.Background(), "/var/lib/gost-deploy/api-token", method, path, body, rev)
	if e != nil {
		t.Fatal("management API", path, e)
	}
	return b, r
}
func verifyOperationsAPI(t *testing.T) {
	t.Helper()
	response, e := http.Get("http://127.0.0.1:18080/")
	if e != nil {
		t.Fatal(e)
	}
	html, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(html), "RelayHarbor 控制台") {
		t.Fatal("embedded console missing")
	}
	original := load(t, "/etc/gost-deploy/config.json")
	originalAPIPID := pid(t, "gost-deploy-api")
	_, rev := apiCall(t, "GET", "/config", nil, "")
	for _, verb := range []string{"stop", "start", "restart"} {
		b, _ := apiCall(t, "POST", "/service/"+verb, map[string]any{}, rev)
		var status map[string]any
		json.Unmarshal(b, &status)
		if status["active"] != (verb != "stop") {
			t.Fatal("wrong service state", verb)
		}
		if pid(t, "gost-deploy-api") != originalAPIPID {
			t.Fatal("service operation restarted API")
		}
		if verb != "stop" {
			testkit.CheckAuth(t, original)
		}
	}
	changed := original.Clone()
	changed.Profiles = append(changed.Profiles, config.Profile{Name: "api-test", Type: "direct"})
	_, rev = apiCall(t, "PUT", "/config", changed, rev)
	_, rev = apiCall(t, "POST", "/rollback", map[string]any{}, rev)
	if !load(t, "/etc/gost-deploy/config.json").Equal(original) {
		t.Fatal("API rollback did not restore original config")
	}
	if _, _, e = api.Client(context.Background(), "/var/lib/gost-deploy/api-token", "POST", "/service/stop", map[string]any{}, "stale"); e == nil {
		t.Fatal("stale service operation accepted")
	}
	if !manage.New().Active(context.Background()) {
		t.Fatal("stale request stopped service")
	}
	b, _ := apiCall(t, "GET", "/logs?limit=100", nil, "")
	var logs manage.LogResult
	if e = json.Unmarshal(b, &logs); e != nil || len(logs.Entries) > 100 {
		t.Fatal("invalid bounded logs", e)
	}
	if len(b) > manage.MaxLogBytes {
		t.Fatal("journal response too large")
	}
	testkit.CheckAuth(t, original)
	t.Log("PASS browser management API: embedded console, service actions, rollback, logs and revision protection")
}

// TestBrowserSession only exposes a test bridge inside a guarded disposable container.
// Publishing its port on host loopback models an SSH tunnel; production stays loopback-only.
func TestBrowserSession(t *testing.T) {
	isolated(t)
	if os.Getenv("GOST_WEB_TEST") != "1" {
		t.Skip("browser fixture only")
	}
	dir := t.TempDir()
	echo := testkit.Echo(t)
	binary := testkit.GOST(t)
	upstreamAuth := config.Auth{Username: "fixture-upstream", Password: "fixture-upstream-password"}
	nodes := []config.Node{}
	for _, kind := range []string{"http", "socks5"} {
		addr := testkit.Addr(t)
		v := native.Service(addr, []config.Auth{upstreamAuth}, false)
		v["services"].([]any)[0].(native.M)["handler"].(native.M)["type"] = kind
		testkit.Start(t, binary, v, addr)
		nodes = append(nodes, config.Node{ID: kind, Type: kind, Addr: addr, Auth: &upstreamAuth})
	}
	c := config.Initial("127.0.0.1:1080", false)
	c.Profiles = append(c.Profiles, config.Profile{Name: "office", Type: "pool", Nodes: nodes, Probe: &config.Probe{Type: "tcp", Addr: echo, Interval: 1, Timeout: 1}})
	path := filepath.Join(dir, "config.json")
	write(t, path, c)
	run(t, "install", "--config", path, "--archive", "/artifacts/gost_3.3.0_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.3.0.txt")
	defer run(t, "uninstall", "--purge")
	held := testkit.Connect(t, c, echo)
	defer held.Close()
	frontPID := pid(t, "gost")
	apiPID := pid(t, "gost-deploy-api")
	var rt supervisor.Status
	manage.New().Runtime(context.Background(), supervisor.Request{Command: "status"}, &rt)
	frontendPID := rt.FrontendPID
	upstream := nodes[0].Addr
	tokenBytes, _ := os.ReadFile("/var/lib/gost-deploy/api-token")
	token := strings.TrimSpace(string(tokenBytes))
	target, _ := url.Parse("http://127.0.0.1:18080")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{Proxy: nil}
	done := make(chan struct{})
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_fixture/") {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			switch r.URL.Path {
			case "/_fixture/info":
				json.NewEncoder(w).Encode(map[string]string{"echo": echo, "upstream": upstream})
			case "/_fixture/check":
				var current supervisor.Status
				manage.New().Runtime(r.Context(), supervisor.Request{Command: "status"}, &current)
				ok := testkit.Payload(held) == nil && frontPID == pid(t, "gost") && frontendPID == current.FrontendPID
				if !ok {
					w.WriteHeader(500)
				}
				json.NewEncoder(w).Encode(map[string]bool{"preserved": ok})
			case "/_fixture/done":
				once.Do(func() { close(done) })
				json.NewEncoder(w).Encode(map[string]bool{"done": true})
			default:
				w.WriteHeader(404)
			}
			return
		}
		proxy.ServeHTTP(w, r)
	})
	listener, e := net.Listen("tcp", "0.0.0.0:18081")
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	t.Log("READY browser fixture")
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	if pid(t, "gost-deploy-api") != apiPID {
		t.Fatal("browser operations restarted management API")
	}
	t.Log("PASS real browser session and independent management service")
}
