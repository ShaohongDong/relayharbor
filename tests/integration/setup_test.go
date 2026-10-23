package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/manage"
)

func verifySetupAccess(t *testing.T) {
	t.Helper()
	m := manage.New()
	cert, e := m.AccessCertificate()
	if e != nil {
		t.Fatal(e)
	}
	root, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	client.Jar, _ = cookiejar.New(nil)
	code, e := m.ResetAuth(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	csrf := ""
	call := func(method, path, body, rev, key string) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, "https://localhost:18443/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Origin", "https://localhost:18443")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if rev != "" {
			r.Header.Set("If-Match", `"`+rev+`"`)
		}
		r.Header.Set("Idempotency-Key", key)
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		return resp.StatusCode, b
	}
	status, b := call("POST", "/auth/initialize", `{"code":"`+code+`","password":"first browser administrator password"}`, "", "")
	if status != 200 {
		t.Fatalf("initialize %d %s", status, b)
	}
	var login struct {
		Data struct {
			CSRF string `json:"csrf"`
		} `json:"data"`
	}
	json.Unmarshal(b, &login)
	csrf = login.Data.CSRF
	status, _ = call("POST", "/auth/initialize", `{"code":"`+code+`","password":"another browser password"}`, "", "")
	if status != 400 {
		t.Fatal("claim reused", status)
	}
	if _, e = os.Stat("/var/lib/gost-deploy/api-token"); !os.IsNotExist(e) {
		t.Fatal("legacy management token retained")
	}
	socket, e := os.Stat(m.APISocket())
	if e != nil || socket.Mode().Perm() != 0600 {
		t.Fatal("local socket permissions", e)
	}
	status, b = call("GET", "/setup", "", "", "")
	if status != 200 {
		t.Fatal("setup status", status)
	}
	var envelope struct {
		Revision string `json:"revision"`
	}
	json.Unmarshal(b, &envelope)
	status, _ = call("POST", "/setup/complete", "{}", "stale", "")
	if status != 409 {
		t.Fatal("stale setup completion accepted")
	}
	status, _ = call("POST", "/setup/complete", "{}", envelope.Revision, "")
	if status != 200 {
		t.Fatal("basic setup completion", status)
	}
	// The wizard is closed for good once completed; AnyTLS initialization stays available.
	for _, path := range []string{"/setup/complete", "/setup/credentials", "/tls/setup"} {
		status, b = call("POST", path, "{}", envelope.Revision, "integration-closed-setup")
		if status != 409 || !strings.Contains(string(b), "setup_completed") {
			t.Fatalf("%s after completion: %d %s", path, status, b)
		}
	}
	// Without HTTPS, AnyTLS must fail as a durable background job; retries of
	// the same request return that job rather than launching a second process.
	status, b = call("POST", "/anytls/setup", "{}", envelope.Revision, "integration-idempotent-job")
	if status != 202 {
		t.Fatalf("job start %d %s", status, b)
	}
	var job struct {
		Data manage.SetupJob `json:"data"`
	}
	json.Unmarshal(b, &job)
	id := job.Data.ID
	for i := 0; i < 100; i++ {
		status, b = call("GET", "/setup/jobs/"+id, "", "", "")
		json.Unmarshal(b, &job)
		if job.Data.Status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != 200 || job.Data.Status != "failed" {
		t.Fatal("background failure not durable", job.Data)
	}
	status, b = call("POST", "/anytls/setup", "{}", envelope.Revision, "integration-idempotent-job")
	json.Unmarshal(b, &job)
	if status != 202 || job.Data.ID != id || job.Data.Status != "failed" {
		t.Fatal("idempotent request replayed", status)
	}
	if _, e = m.ResetAuth(context.Background()); e != nil {
		t.Fatal(e)
	}
	status, _ = call("GET", "/config", "", "", "")
	if status != 401 {
		t.Fatal("password reset retained session")
	}
	t.Log("PASS setup HTTPS + single-use claim + password sessions + root socket + persistent idempotent task")
}
