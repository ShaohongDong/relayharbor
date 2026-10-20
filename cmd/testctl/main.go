// testctl fetches fixtures and binds acceptance logs to a source snapshot.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func sources() (map[string]string, error) {
	out := map[string]string{}
	e := filepath.WalkDir(".", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "playwright-report" || path == "internal/webui/assets" {
				return filepath.SkipDir
			}
			if path != "." && (strings.HasPrefix(d.Name(), ".") && d.Name() != ".github" || d.Name() == "bin" || d.Name() == "dist" || d.Name() == "test-results") {
				return filepath.SkipDir
			}
			return nil
		}
		if path == "docs/validation.md" || path == "AGENTS.md" || strings.HasSuffix(path, ".local.json") || path == "gost.json" {
			return nil
		}
		h, e := manage.Digest(path)
		if e != nil {
			return e
		}
		out[path] = h
		return nil
	})
	return out, e
}
func load(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}

// Publish cache entries only after a complete download, so failures can be retried.
func cacheDownload(url, path string) error {
	if _, e := os.Stat(path); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	tmp := path + ".tmp"
	if e := os.Remove(tmp); e != nil && !os.IsNotExist(e) {
		return e
	}
	defer os.Remove(tmp)
	if e := manage.Download(context.Background(), url, tmp); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

func fetch() error {
	os.MkdirAll(".cache/releases", 0755)
	for _, v := range []string{"3.3.0", "3.2.6"} {
		base := "https://github.com/go-gost/gost/releases/download/v" + v + "/"
		sums := filepath.Join(".cache/releases", "checksums-"+v+".txt")
		if e := cacheDownload(base+"checksums.txt", sums); e != nil {
			return e
		}
		for _, arch := range []string{"amd64", "arm64"} {
			name := "gost_" + v + "_linux_" + arch + ".tar.gz"
			path := filepath.Join(".cache/releases", name)
			if e := cacheDownload(base+name, path); e != nil {
				return e
			}
			want, e := manage.Checksum(sums, name)
			if e != nil {
				return e
			}
			got, e := manage.Digest(path)
			if e != nil || want != got || (v == manage.DefaultVersion && want != manage.Pinned[arch]) {
				return errors.New("fixture checksum mismatch")
			}
			fmt.Println("PASS verified", name)
		}
	}
	base := "https://github.com/go-acme/lego/releases/download/v" + manage.LEGOVersion + "/"
	sums := filepath.Join(".cache/releases", "lego_"+manage.LEGOVersion+"_checksums.txt")
	if e := cacheDownload(base+filepath.Base(sums), sums); e != nil {
		return e
	}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "lego_v" + manage.LEGOVersion + "_linux_" + arch + ".tar.gz"
		path := filepath.Join(".cache/releases", name)
		if e := cacheDownload(base+name, path); e != nil {
			return e
		}
		want, e := manage.Checksum(sums, name)
		got, ge := manage.Digest(path)
		if e != nil || ge != nil || got != want || want != manage.LEGOPinned[arch] {
			return errors.New("lego fixture checksum mismatch")
		}
		fmt.Println("PASS verified", name)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "sing-box-" + manage.SingBoxVersion + "-linux-" + arch + ".tar.gz"
		path := filepath.Join(".cache/releases", name)
		if e := cacheDownload("https://github.com/SagerNet/sing-box/releases/download/v"+manage.SingBoxVersion+"/"+name, path); e != nil {
			return e
		}
		got, e := manage.Digest(path)
		if e != nil || got != manage.SingBoxPinned[arch] {
			return errors.New("sing-box fixture checksum mismatch")
		}
		fmt.Println("PASS verified", name)
		clientArch, want := "amd64-compatible", "ba3ce607747a07f948fc35780e108a4a7c7f552a38b9bd4d115f313ebcb89c20"
		if arch == "arm64" {
			clientArch, want = "arm64", "9dd862e28b46ff7d775f169cceebc28deccaa0a9e804237d421cd2571e0caba0"
		}
		name = "mihomo-linux-" + clientArch + "-v1.19.32.gz"
		path = filepath.Join(".cache/releases", name)
		if e := cacheDownload("https://github.com/MetaCubeX/mihomo/releases/download/v1.19.32/"+name, path); e != nil {
			return e
		}
		got, e = manage.Digest(path)
		if e != nil || got != want {
			return errors.New("mihomo fixture checksum mismatch")
		}
		fmt.Println("PASS verified", name)
	}
	return nil
}
func evidence(mode string) error {
	s, e := sources()
	if e != nil {
		return e
	}
	os.MkdirAll("test-results", 0755)
	baseline := "test-results/source-baseline.json"
	if mode == "start" {
		if e := os.Remove("test-results/evidence.json"); e != nil && !os.IsNotExist(e) {
			return e
		}
		return os.WriteFile(baseline, config.Bytes(s), 0600)
	}
	var old map[string]string
	if e = load(baseline, &old); e != nil {
		return e
	}
	if !reflect.DeepEqual(s, old) {
		return errors.New("sources changed during campaign")
	}
	logs := map[string]any{}
	for _, name := range []string{"ubuntu-20-04", "ubuntu-22-04", "ubuntu-24-04", "debian-12-slim", "debian-13-slim", "ubuntu-24-04-arm64-qemu", "debian-12-slim-arm64-qemu"} {
		path := "test-results/" + name + ".log"
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		content := string(b)
		for _, marker := range []string{"PASS removed workers drain existing streams before exit", "PASS AnyTLS real Mihomo TCP + verified TLS + authentication + server-side UDP rejection", "PASS\n"} {
			if !strings.Contains(content, marker) {
				return fmt.Errorf("%s missing %s", name, marker)
			}
		}
		if !strings.Contains(name, "arm64") {
			for _, marker := range []string{"PASS lifecycle acceptance", "PASS boot verification", "PASS manager update and rollback", "PASS browser management API", "PASS real nginx + lego + HTTP-01 + CA-verified HTTPS + systemd renewal + held SOCKS", "PASS HTTPS and persistent renewal timer after container reboot", "PASS AnyTLS Mihomo subscription + user isolation + token/password revocation + pool failover/fail-closed", "PASS AnyTLS certificate hot reload + held Mihomo stream + unchanged instance PID", "PASS AnyTLS independent systemd instance reboot recovery"} {
				if !strings.Contains(content, marker) {
					return fmt.Errorf("%s missing %s", name, marker)
				}
			}
		} else {
			if !strings.Contains(content, "gostctl ") || !strings.Contains(content, "linux/arm64") {
				return errors.New("ARM64 manager execution missing")
			}
			if !strings.Contains(content, "lego version 5.5.2") {
				return errors.New("ARM64 lego execution missing")
			}
		}
		hash, _ := manage.Digest(path)
		logs[name] = map[string]any{"logSha256": hash, "passed": true, "routingScenarios": 9, "hardwareAcceptance": false}
	}
	for _, name := range []string{"browser-real", "browser-systemd"} {
		path := filepath.Join("test-results", name+".log")
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		marker := "1 passed"
		if name == "browser-systemd" {
			marker = "PASS real browser session and independent management service"
		}
		if !strings.Contains(string(b), marker) {
			return fmt.Errorf("missing browser acceptance: %s", name)
		}
		hash, _ := manage.Digest(path)
		logs[name] = map[string]any{"logSha256": hash, "passed": true}
	}
	binaries := map[string]string{}
	qemu := os.Getenv("GOST_TEST_QEMU_BIN")
	if qemu == "" {
		return errors.New("missing QEMU binary binding")
	}
	qemuHash, e := manage.Digest(qemu)
	if e != nil {
		return e
	}
	binaries[qemu] = qemuHash
	for _, path := range []string{"bin/gostctl", ".cache/test-bin/integration.test", ".cache/test-bin/runtime-amd64.test", ".cache/test-bin/runtime-arm64.test", ".cache/test-bin/subscription-amd64.test", ".cache/test-bin/subscription-arm64.test", ".cache/test-bin/gostctl-arm64", ".cache/test-bin/gostctl-previous", ".cache/test-bin/pebble", ".cache/test-bin/lego-arm64"} {
		hash, e := manage.Digest(path)
		if e != nil {
			return e
		}
		binaries[path] = hash
	}
	assets := map[string]string{}
	if e := filepath.WalkDir("internal/webui/assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		h, err := manage.Digest(path)
		if err == nil {
			assets[path] = h
		}
		return err
	}); e != nil {
		return e
	}
	report := map[string]any{"createdAt": time.Now().UTC().Format(time.RFC3339), "sources": s, "binaries": binaries, "webAssets": assets, "environments": logs}
	if e = os.WriteFile("test-results/evidence.json", config.Bytes(report), 0600); e == nil {
		fmt.Println("PASS source-bound evidence: test-results/evidence.json")
	}
	return e
}
func main() {
	var e error
	if len(os.Args) == 2 && os.Args[1] == "fetch" {
		e = fetch()
	} else if len(os.Args) == 3 && os.Args[1] == "evidence" && (os.Args[2] == "start" || os.Args[2] == "finish") {
		e = evidence(os.Args[2])
	} else {
		e = errors.New("usage: testctl fetch | evidence start|finish")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
