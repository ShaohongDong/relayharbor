package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCacheDownloadRetriesInterruptedManifest(t *testing.T) {
	const manifest = "complete checksum manifest\n"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			// An early EOF after headers and some bytes simulates a dropped download.
			w.Header().Set("Content-Length", "1000")
			fmt.Fprint(w, "partial")
			return
		}
		fmt.Fprint(w, manifest)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "checksums-3.3.0.txt")
	if err := cacheDownload(server.URL+"/checksums.txt", path); err == nil {
		t.Fatal("interrupted download unexpectedly succeeded")
	}
	for _, p := range []string{path, path + ".tmp"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("failed download left a cache entry at %s: %v", p, err)
		}
	}
	// A process terminated during download may leave a temporary file behind.
	if err := os.WriteFile(path+".tmp", []byte("stale partial download"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cacheDownload(server.URL+"/checksums.txt", path); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != manifest {
		t.Fatalf("cached incomplete content: %q, %v", b, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary download not removed: %v", err)
	}
	if err := cacheDownload(server.URL+"/checksums.txt", path); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("expected one failed download and one successful retry, got %d requests", requests.Load())
	}
}
