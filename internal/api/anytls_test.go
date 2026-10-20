package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShaohongDong/relayharbor/internal/manage"
)

type anyFake struct {
	*fake
	op  string
	req manage.AnyTLSRequest
}

func (f *anyFake) AnyTLS(_ context.Context, op string, req manage.AnyTLSRequest, expected string) (any, string, error) {
	if op != "status" && expected != "a1" {
		return nil, "", manage.ErrAnyTLSRevision
	}
	f.op, f.req = op, req
	return manage.AnyTLSStatus{Configured: true, Users: []manage.AnyTLSUserStatus{}}, "a1", nil
}
func (f *anyFake) Subscription(_ context.Context, token string, provider bool) ([]byte, error) {
	if token != strings.Repeat("a", 43) {
		return nil, errors.New("not found")
	}
	return []byte("proxies: []\n"), nil
}
func TestAnyTLSAPI(t *testing.T) {
	f := &anyFake{fake: newFake()}
	server := httptest.NewServer(localTestHandler(f, "token"))
	defer server.Close()
	for _, tc := range []struct {
		method, path, token, rev, body string
		status                         int
	}{
		{"GET", "/anytls", "", "", "", 401},
		{"GET", "/anytls", "token", "", "", 200},
		{"POST", "/anytls/users", "token", "", `{"id":"a","name":"设备"}`, 428},
		{"POST", "/anytls/users", "token", "r1", `{"id":"a","name":"设备"}`, 409},
		{"POST", "/anytls/users", "token", "a1", `{"id":"a","name":"设备","port":8443}`, 200},
		{"POST", "/anytls/users/a/disable", "token", "a1", `{"password":"oops"}`, 400},
		{"POST", "/anytls/users/a/disable", "token", "a1", `{}`, 200},
		{"POST", "/anytls/users/a/credentials", "token", "a1", `{}`, 200},
		{"POST", "/anytls/users", "token", "a1", `null`, 400},
		{"POST", "/anytls/users", "token", "a1", `{"port":1.5}`, 400},
		{"POST", "/anytls/users", "token", "a1", `{"id":"a","name":"设备","port":null}`, 400},
	} {
		code, _, _ := request(t, server.URL, tc.method, tc.path, tc.token, tc.rev, []byte(tc.body))
		if code != tc.status {
			t.Fatalf("%s %s: %d want %d", tc.method, tc.path, code, tc.status)
		}
	}
	if f.s.Revision != "r1" {
		t.Fatal("independent revision changed core")
	}
}
func TestSubscriptionTransportAndToken(t *testing.T) {
	h := localTestHandler(&anyFake{fake: newFake()}, "admin")
	for _, tc := range []struct {
		path, proto, remote, method string
		code                        int
	}{
		{"/sub/" + strings.Repeat("a", 43) + "/clash.yaml", "https", "127.0.0.1:1234", "GET", 200},
		{"/sub/" + strings.Repeat("a", 43) + "/proxies.yaml", "https", "127.0.0.1:1234", "GET", 200},
		{"/sub/" + strings.Repeat("a", 43) + "/clash.yaml", "", "127.0.0.1:1234", "GET", 404},
		{"/sub/" + strings.Repeat("a", 43) + "/clash.yaml", "https", "192.0.2.1:1234", "GET", 404},
		{"/sub/admin/clash.yaml", "https", "127.0.0.1:1234", "GET", 404},
		{"/sub/" + strings.Repeat("b", 43) + "/clash.yaml", "https", "127.0.0.1:1234", "GET", 404},
		{"/sub/" + strings.Repeat("a", 43) + "/clash.yaml?q=1", "https", "127.0.0.1:1234", "GET", 404},
		{"/sub/" + strings.Repeat("a", 43) + "/clash.yaml", "https", "127.0.0.1:1234", "POST", 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code)
		}
	}
}
