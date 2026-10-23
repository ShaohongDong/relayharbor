package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaohongDong/relayharbor/internal/manage"
)

func TestSetupReadOnlyRoutesRejectMutations(t *testing.T) {
	h := &Handler{Backend: manage.New()}
	for _, path := range []string{"/setup", "/setup/jobs/0123456789abcdef0123456789abcdef"} {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			r := httptest.NewRequest(method, Prefix+path, nil)
			w := httptest.NewRecorder()
			if !h.setup(w, r) || w.Code != 405 {
				t.Fatalf("%s %s: got %d, want 405", method, path, w.Code)
			}
		}
	}
}

func TestSetupWizardClosedAfterCompletion(t *testing.T) {
	m := manage.New()
	root := t.TempDir()
	m.P.State = filepath.Join(root, "state.json")
	if e := os.WriteFile(filepath.Join(root, "access.json"), []byte(`{"host":"192.0.2.5","port":18443,"completed":true}`), 0600); e != nil {
		t.Fatal(e)
	}
	h := &Handler{Backend: m}
	for _, path := range []string{"/setup/complete", "/setup/credentials", "/tls/setup"} {
		r := httptest.NewRequest("POST", Prefix+path, strings.NewReader("{}"))
		r.Header.Set("If-Match", `"r1"`)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		if !h.setup(w, r) || w.Code != 409 || !strings.Contains(w.Body.String(), "setup_completed") {
			t.Fatalf("%s: got %d %s", path, w.Code, w.Body)
		}
	}
}
