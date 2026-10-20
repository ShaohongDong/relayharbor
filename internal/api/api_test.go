package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"github.com/ShaohongDong/relayharbor/internal/supervisor"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fake struct {
	mu               sync.Mutex
	c                config.Config
	s                manage.State
	entered, release chan struct{}
	managedAPI       bool
	active           bool
	serviceVerb      string
	tlsError         error
}

func newFake() *fake {
	return &fake{c: config.Initial("127.0.0.1:1080", false), s: manage.State{Schema: 3, Revision: "r1", Installed: true}}
}
func (f *fake) Locked(fn func() error) error                 { f.mu.Lock(); defer f.mu.Unlock(); return fn() }
func (f *fake) Load(bool) (*manage.State, error)             { s := f.s; return &s, nil }
func (f *fake) Managed(context.Context, *manage.State) error { return nil }
func (f *fake) ReadConfig() (config.Config, error)           { return f.c.Clone(), nil }
func (f *fake) Active(context.Context) bool                  { return f.active }
func (f *fake) Status(context.Context, *manage.State) (map[string]any, error) {
	return map[string]any{"activeProfile": f.c.ActiveProfile}, nil
}
func (f *fake) Preflight(config.Config, *config.Config) error { return nil }
func (f *fake) Apply(_ context.Context, _ *manage.State, _ manage.Release, c config.Config, _ *manage.Snapshot, manageAPI bool) error {
	f.managedAPI = manageAPI
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	f.c = c
	f.s.Revision = "r2"
	return nil
}
func (f *fake) Runtime(context.Context, supervisor.Request, any) error { return nil }
func (f *fake) Service(_ context.Context, verb string) error {
	f.serviceVerb = verb
	f.active = verb != "stop"
	return nil
}
func (f *fake) Rollback(context.Context, *manage.State) error {
	f.s.Revision = "rolled-back"
	return nil
}
func (f *fake) Logs(_ context.Context, _ *manage.State, _ config.Config, limit int) (manage.LogResult, error) {
	return manage.LogResult{Entries: []manage.LogEntry{{Message: "bounded logs"}}}, nil
}
func (f *fake) TLSStatus(context.Context) (manage.TLSStatus, error) { return manage.TLSStatus{}, nil }
func (f *fake) TLSStart(_ context.Context, op string) error         { f.serviceVerb = op; return f.tlsError }
func request(t *testing.T, url, method, path, token, rev string, body []byte) (int, map[string]any, http.Header) {
	t.Helper()
	r, e := http.NewRequest(method, url+Prefix+path, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if rev != "" {
		r.Header.Set("If-Match", `"`+rev+`"`)
	}
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return resp.StatusCode, v, resp.Header
}
func TestAPIContract(t *testing.T) {
	f := newFake()
	f.c.Profiles = append(f.c.Profiles, config.Profile{Name: "second", Type: "direct"})
	server := httptest.NewServer(&Handler{f, "token"})
	defer server.Close()
	status, v, h := request(t, server.URL, "GET", "/config", "token", "", nil)
	if status != 200 || h.Get("ETag") != `"r1"` || bytes.Contains(config.Bytes(v), []byte(f.c.Listener.Users[0].Password)) {
		t.Fatal("redaction/etag")
	}
	if s, _, _ := request(t, server.URL, "GET", "/config", "wrong", "", nil); s != 401 {
		t.Fatal("authentication")
	}
	for _, tt := range []struct {
		revision string
		want     int
	}{{"", 428}, {"stale", 409}, {"r1", 200}} {
		s, _, _ := request(t, server.URL, "PUT", "/active-profile", "token", tt.revision, []byte(`{"name":"second"}`))
		if s != tt.want {
			t.Fatalf("revision %s status %d", tt.revision, s)
		}
	}
	if f.managedAPI || f.c.ActiveProfile != "second" {
		t.Fatal("apply behavior")
	}
}

func TestTLSContract(t *testing.T) {
	f := newFake()
	server := httptest.NewServer(&Handler{f, "token"})
	defer server.Close()
	if s, _, _ := request(t, server.URL, "GET", "/tls", "bad", "", nil); s != 401 {
		t.Fatal("TLS auth", s)
	}
	if s, v, _ := request(t, server.URL, "GET", "/tls", "token", "", nil); s != 200 || v["revision"] != "r1" {
		t.Fatal("TLS status", s, v)
	}
	for _, op := range []string{"renew", "reload"} {
		for _, tt := range []struct {
			rev  string
			want int
		}{{"", 428}, {"stale", 409}, {"r1", 200}} {
			s, v, _ := request(t, server.URL, "POST", "/tls/"+op, "token", tt.rev, []byte(`{}`))
			if s != tt.want {
				t.Fatal(op, tt, s, v)
			}
			if s == 200 && (v["revision"] != "r1" || f.serviceVerb != op) {
				t.Fatal("TLS changed proxy revision")
			}
		}
		if s, _, _ := request(t, server.URL, "POST", "/tls/"+op, "token", "r1", []byte(`{"force":true}`)); s != 400 {
			t.Fatal("unexpected input accepted", s)
		}
	}
	f.tlsError = manage.ErrTLSBusy
	if s, v, _ := request(t, server.URL, "POST", "/tls/renew", "token", "r1", []byte(`{}`)); s != 409 || v["error"].(map[string]any)["code"] != "tls_busy" {
		t.Fatal("duplicate TLS job", s, v)
	}
}
func TestValidateAndMalformed(t *testing.T) {
	f := newFake()
	server := httptest.NewServer(&Handler{f, "token"})
	defer server.Close()
	s, _, _ := request(t, server.URL, "POST", "/config/validate", "token", "", config.Bytes(f.c.Redacted()))
	if s != 200 {
		t.Fatal("redacted validate")
	}
	if f.s.Revision != "r1" {
		t.Fatal("validate mutated")
	}
	for _, body := range []string{`{"name":"a","name":"b"}`, `{} {}`, `null`, `{"name":null}`, `{"name":"missing"}`, `{"Name":"direct"}`} {
		s, _, _ := request(t, server.URL, "PUT", "/active-profile", "token", "r1", []byte(body))
		if s != 400 {
			t.Fatalf("malformed accepted: %s %d", body, s)
		}
	}
	if s, _, _ := request(t, server.URL, "GET", "/config?token=token", "token", "", nil); s != 404 {
		t.Fatal("query accepted")
	}
}
func TestConcurrentRevision(t *testing.T) {
	f := newFake()
	f.c.Profiles = append(f.c.Profiles, config.Profile{Name: "second", Type: "direct"})
	server := httptest.NewServer(&Handler{f, "token"})
	defer server.Close()
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			s, _, _ := request(t, server.URL, "PUT", "/active-profile", "token", "r1", []byte(`{"name":"second"}`))
			codes <- s
		}()
	}
	a, b := <-codes, <-codes
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("concurrent lost update: %d %d", a, b)
	}
}
func TestShutdownWaitsForMutation(t *testing.T) {
	f := newFake()
	f.c.Profiles = append(f.c.Profiles, config.Profile{Name: "second", Type: "direct"})
	f.entered = make(chan struct{})
	f.release = make(chan struct{})
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeListener(ctx, l, &Handler{f, "token"}) }()
	response := make(chan int, 1)
	go func() {
		s, _, _ := request(t, "http://"+l.Addr().String(), "PUT", "/active-profile", "token", "r1", []byte(`{"name":"second"}`))
		response <- s
	}()
	<-f.entered
	cancel()
	select {
	case <-done:
		t.Fatal("transaction truncated")
	case <-time.After(80 * time.Millisecond):
	}
	close(f.release)
	if <-response != 200 {
		t.Fatal("transaction failed")
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

func TestOperationsContract(t *testing.T) {
	f := newFake()
	server := httptest.NewServer(&Handler{f, "token"})
	defer server.Close()
	for _, path := range []string{"/service/start", "/service/stop", "/service/restart", "/rollback"} {
		for _, tt := range []struct {
			token, rev string
			want       int
		}{{"wrong", "r1", 401}, {"token", "", 428}, {"token", "old", 409}} {
			code, _, _ := request(t, server.URL, "POST", path, tt.token, tt.rev, []byte(`{}`))
			if code != tt.want {
				t.Fatalf("%s %d != %d", path, code, tt.want)
			}
		}
		for _, body := range []string{`null`, `[]`, `{"verb":"start"}`} {
			code, _, _ := request(t, server.URL, "POST", path, "token", "r1", []byte(body))
			if code != 400 {
				t.Fatalf("bad operation body %s %d", body, code)
			}
		}
	}
	for _, verb := range []string{"start", "stop", "restart"} {
		code, _, _ := request(t, server.URL, "POST", "/service/"+verb, "token", "r1", []byte(`{}`))
		if code != 200 || f.serviceVerb != verb {
			t.Fatal("service action", verb, code)
		}
	}
	if code, _, _ := request(t, server.URL, "POST", "/service/enable", "token", "r1", []byte(`{}`)); code != 404 {
		t.Fatal("unknown action accepted")
	}
	if code, _, _ := request(t, server.URL, "POST", "/rollback", "token", "r1", []byte(`{}`)); code != 409 {
		t.Fatal("missing rollback snapshot")
	}
	f.s.Previous = &manage.Snapshot{}
	code, body, h := request(t, server.URL, "POST", "/rollback", "token", "r1", []byte(`{}`))
	if code != 200 || body["revision"] != "rolled-back" || h.Get("ETag") != `"rolled-back"` {
		t.Fatal("rollback revision", code, body)
	}
	for _, tt := range []struct {
		path string
		want int
	}{{"/logs", 200}, {"/logs?limit=500", 200}, {"/logs?limit=0", 400}, {"/logs?limit=501", 400}, {"/logs?limit=no", 400}, {"/logs?limit=1&limit=2", 404}, {"/logs?unit=ssh", 404}, {"/logs?limit=1&token=token", 404}} {
		code, _, _ := request(t, server.URL, "GET", tt.path, "token", "", nil)
		if code != tt.want {
			t.Fatalf("%s %d != %d", tt.path, code, tt.want)
		}
	}
}
