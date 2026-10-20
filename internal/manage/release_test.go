package manage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrepareReleaseValidatesCachedPath(t *testing.T) {
	for _, cache := range []string{"fresh", "regular", "symlink-inside", "symlink-outside", "tampered"} {
		t.Run(cache, func(t *testing.T) {
			m := fixtureManager(t)
			const version = "3.2.6"
			binary := []byte("verified release fixture\n")
			name := fmt.Sprintf("gost_%s_linux_%s.tar.gz", version, runtime.GOARCH)
			dir := t.TempDir()
			archive := filepath.Join(dir, name)
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: "gost", Mode: 0755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(binary); err != nil {
				t.Fatal(err)
			}
			for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
				if err := close(); err != nil {
					t.Fatal(err)
				}
			}
			hash, err := Digest(archive)
			if err != nil {
				t.Fatal(err)
			}
			sums := filepath.Join(dir, "checksums.txt")
			if err := os.WriteFile(sums, []byte(hash+"  "+name+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			m.Exec = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte(fmt.Sprintf("gost %s (go1.27.2 linux/%s)", version, runtime.GOARCH)), nil
			}
			dest := filepath.Join(m.P.Releases, version, runtime.GOARCH, "gost")
			if cache != "fresh" {
				if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
					t.Fatal(err)
				}
				switch cache {
				case "regular", "tampered":
					data := binary
					if cache == "tampered" {
						data = []byte("modified release")
					}
					if err := os.WriteFile(dest, data, 0755); err != nil {
						t.Fatal(err)
					}
				default:
					target := filepath.Join(dir, "same-binary")
					if cache == "symlink-inside" {
						target = filepath.Join(filepath.Dir(dest), "same-binary")
					}
					if err := os.WriteFile(target, binary, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, dest); err != nil {
						t.Fatal(err)
					}
				}
			}
			r, err := m.PrepareRelease(context.Background(), version, archive, sums)
			if cache == "fresh" || cache == "regular" {
				if err != nil {
					t.Fatal(err)
				}
				if err := m.VerifyRelease(r); err != nil {
					t.Fatalf("prepared release failed subsequent verification: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("accepted a modified or symlinked release cache")
				}
				if cache != "tampered" {
					if _, err := os.Readlink(dest); err != nil {
						t.Fatalf("rejected cache was overwritten: %v", err)
					}
				}
			}
		})
	}
}
