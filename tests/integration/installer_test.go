package integration

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func verifyInstallerDownload(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script, e := os.ReadFile("/src/install.sh")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "install.sh"), script, 0700); e != nil {
		t.Fatal(e)
	}
	binary, e := os.ReadFile(ctl())
	if e != nil {
		t.Fatal(e)
	}
	binarySum := fmt.Sprintf("%x  gostctl\n", sha256.Sum256(binary))
	archive := filepath.Join(dir, "relayharbor_0.0.0_linux_amd64.tar.gz")
	f, e := os.Create(archive)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, v := range []struct {
		name string
		mode int64
		data []byte
	}{{"bin/gostctl", 0755, binary}, {"bin/SHA256SUMS", 0644, []byte(binarySum)}} {
		if e = tw.WriteHeader(&tar.Header{Name: "relayharbor_0.0.0_linux_amd64/" + v.name, Mode: v.mode, Size: int64(len(v.data))}); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(v.data); e != nil {
			t.Fatal(e)
		}
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	gz.Close()
	f.Close()
	b, _ := os.ReadFile(archive)
	sum := fmt.Sprintf("%x  %s\n", sha256.Sum256(b), filepath.Base(archive))
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sum), 0600)
	// Only the release-download transport is replaced. The real Bash entrypoint,
	// checksums, extraction, Go install, systemd and HTTPS readiness all execute.
	shim := filepath.Join(dir, "tools")
	os.Mkdir(shim, 0700)
	curl := `#!/bin/bash
set -euo pipefail
output=''
url=''
while (($#)); do
 case "$1" in --output) output=$2; shift 2;; https://*) url=$1; shift;; *) shift;; esac
done
case "$url" in
 https://github.com/ShaohongDong/relayharbor/releases/download/v0.0.0/SHA256SUMS) cp "$INSTALL_FIXTURE/SHA256SUMS" "$output";;
 https://github.com/ShaohongDong/relayharbor/releases/download/v0.0.0/relayharbor_0.0.0_linux_amd64.tar.gz) cp "$INSTALL_FIXTURE/relayharbor_0.0.0_linux_amd64.tar.gz" "$output";;
 *) exit 1;;
esac
`
	os.WriteFile(filepath.Join(shim, "curl"), []byte(curl), 0700)
	args := []string{filepath.Join(dir, "install.sh"), "--non-interactive", "--release-version", "0.0.0", "--setup-host", "localhost", "--archive", "/artifacts/gost_3.3.0_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.3.0.txt"}
	execute := func(ok bool) {
		t.Helper()
		cmd := exec.Command("bash", args...)
		cmd.Env = append(os.Environ(), "PATH="+shim+":"+os.Getenv("PATH"), "INSTALL_FIXTURE="+dir)
		out, e := cmd.CombinedOutput()
		if (e == nil) != ok {
			t.Fatalf("installer outcome %v: %s", e, out)
		}
		if ok && !strings.Contains(string(out), "一次性初始化码") {
			t.Fatal("missing browser handoff")
		}
	}
	// Corruption is rejected before core installation, then a corrected retry succeeds.
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Repeat("0", 64)+"  "+filepath.Base(archive)+"\n"), 0600)
	execute(false)
	if _, e = os.Stat("/etc/systemd/system/gost.service"); !os.IsNotExist(e) {
		t.Fatal("corrupt download installed a service")
	}
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sum), 0600)
	execute(true)
	before, e := os.ReadFile("/etc/gost-deploy/config.json")
	if e != nil {
		t.Fatal(e)
	}
	execute(true)
	after, _ := os.ReadFile("/etc/gost-deploy/config.json")
	if string(before) != string(after) {
		t.Fatal("reinstall reset configuration")
	}
	run(t, "uninstall", "--purge")
	t.Log("PASS standalone installer download/checksum/retry/default config/idempotence")
}
