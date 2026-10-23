package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClaimAtomicAndPasswordStorage(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "auth.json")}
	code, e := s.Claim(false)
	if e != nil {
		t.Fatal(e)
	}
	const p = "long administrator password"
	var wg sync.WaitGroup
	success := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); success <- s.Initialize(code, p) == nil }()
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatal("claim was not single-use", n)
	}
	b, _ := os.ReadFile(s.Path)
	if strings.Contains(string(b), p) || strings.Contains(string(b), code) {
		t.Fatal("plaintext secret persisted")
	}
	if _, e = s.Login("wrong"); e == nil {
		t.Fatal("bad password accepted")
	}
	if _, e = s.Login(p); e != nil {
		t.Fatal(e)
	}
	if c, e := s.Claim(false); e != nil || c != "" {
		t.Fatal("reinstall reset administrator")
	}
	if e = s.Change(p, "another strong password"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Login(p); e == nil {
		t.Fatal("old password accepted")
	}
	if _, e = s.Claim(true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Login("another strong password"); e == nil {
		t.Fatal("reset retained password")
	}
}
func TestBrowserSessionsAndCSRF(t *testing.T) {
	store := &Store{Path: filepath.Join(t.TempDir(), "auth.json")}
	code, _ := store.Claim(false)
	a := New(store)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			a.ServeHTTP(w, r)
			return
		}
		if _, ok := a.Check(r); !ok {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := server.Client()
	client.Jar, _ = cookiejar.New(nil)
	call := func(path, method, origin, csrf, body string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		res, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	payload, _ := json.Marshal(map[string]string{"code": code, "password": "a sufficiently long password"})
	res := call("/api/v1/auth/initialize", "POST", "https://attacker.example", "", string(payload))
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross origin initialize")
	}
	res = call("/api/v1/auth/initialize", "POST", server.URL, "", string(payload))
	var data struct {
		Data struct {
			CSRF string `json:"csrf"`
		} `json:"data"`
	}
	json.NewDecoder(res.Body).Decode(&data)
	res.Body.Close()
	if res.StatusCode != 200 || data.Data.CSRF == "" {
		t.Fatal("initialize", res.StatusCode)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie attributes")
	}
	for _, tc := range []struct {
		origin, csrf string
		status       int
	}{{server.URL, "", 401}, {"https://attacker.example", data.Data.CSRF, 401}, {server.URL, data.Data.CSRF, 200}} {
		r := call("/protected", "POST", tc.origin, tc.csrf, "{}")
		r.Body.Close()
		if r.StatusCode != tc.status {
			t.Fatal(tc, r.StatusCode)
		}
	}
	// The old bearer API is gone, even with a syntactically valid credential.
	r, _ := http.NewRequest("GET", server.URL+"/protected", nil)
	r.Header.Set("Authorization", "Bearer "+code)
	anonymous := &http.Client{Transport: client.Transport}
	res, e := anonymous.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("bearer accepted")
	}
	a.mu.Lock()
	for k, v := range a.sessions {
		v.Seen = time.Now().Add(-31 * time.Minute)
		a.sessions[k] = v
	}
	a.mu.Unlock()
	res = call("/protected", "GET", "", "", "")
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("expired session accepted")
	}
	// API restart has no persisted session secret.
	b, _ := os.ReadFile(store.Path)
	if bytes.Contains(b, []byte(data.Data.CSRF)) {
		t.Fatal("session persisted")
	}
}
func TestPasswordLengthAndRateLimit(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "auth.json")}
	code, _ := s.Claim(false)
	if s.Initialize(code, "short") == nil {
		t.Fatal("short password")
	}
	a := New(s)
	r := httptest.NewRequest("POST", "https://localhost/", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	for i := 0; i < 10; i++ {
		if !a.allowed(r) {
			t.Fatal(i)
		}
	}
	if a.allowed(r) {
		t.Fatal("rate limit")
	}
}
func TestRateLimitGroupsIPv6Prefix(t *testing.T) {
	a := New(&Store{Path: filepath.Join(t.TempDir(), "auth.json")})
	r := httptest.NewRequest("POST", "https://localhost/", nil)
	for i := 0; i < 10; i++ {
		r.RemoteAddr = fmt.Sprintf("[2001:db8:1:2::%x]:1234", i+1)
		if !a.allowed(r) {
			t.Fatal(i)
		}
	}
	r.RemoteAddr = "[2001:db8:1:2:ffff::1]:1234"
	if a.allowed(r) {
		t.Fatal("rotating addresses inside one /64 bypassed the limit")
	}
	for _, addr := range []string{"[2001:db8:1:3::1]:1234", "192.0.2.1:1234", "[::ffff:192.0.2.2]:1234"} {
		r.RemoteAddr = addr
		if !a.allowed(r) {
			t.Fatal("independent client limited", addr)
		}
	}
	if attemptKey("::ffff:192.0.2.2") != "192.0.2.2" {
		t.Fatal("IPv4-mapped address must share the IPv4 bucket")
	}
}
