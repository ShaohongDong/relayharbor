package api

import (
	"github.com/ShaohongDong/relayharbor/internal/auth"
	"net/http"
)

// Lifecycle contract tests use an explicitly local transport. Browser
// authentication and CSRF are exercised separately by internal/auth tests.
func localTestHandler(b Backend, credential string) http.Handler {
	h := &Handler{Backend: b}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Local") == credential {
			r = auth.Local(r)
		}
		h.ServeHTTP(w, r)
	})
}
