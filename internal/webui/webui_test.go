package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticAndAPIRouting(t *testing.T) {
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(401)
	}))
	for _, tt := range []struct {
		path string
		want int
	}{{"/", 200}, {"/missing.js", 404}, {"/assets/", 404}, {"/api/v1/status", 401}, {"/api/missing", 401}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tt.path, nil))
		if w.Code != tt.want {
			t.Fatalf("%s status %d", tt.path, w.Code)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing security headers")
		}
		if tt.path == "/" && (!strings.Contains(w.Body.String(), "RelayHarbor 控制台") || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("console missing or cached")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 405 {
		t.Fatal("static mutation allowed")
	}
}
