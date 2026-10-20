package api

import (
	"net/http/httptest"
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
