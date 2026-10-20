package integration

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"github.com/ShaohongDong/relayharbor/internal/native"
	"github.com/ShaohongDong/relayharbor/internal/testkit"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func ctl() string {
	if p := os.Getenv("GOST_CTL"); p != "" {
		return p
	}
	return "/src/bin/gostctl"
}
func command(t *testing.T, ok bool, binary string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if (e == nil) != ok {
		t.Fatalf("command %s %v: %v\n%s", filepath.Base(binary), args, e, b)
	}
	return b
}
func run(t *testing.T, args ...string) []byte  { t.Helper(); return command(t, true, ctl(), args...) }
func fail(t *testing.T, args ...string) []byte { t.Helper(); return command(t, false, ctl(), args...) }
func sys(t *testing.T, args ...string) []byte {
	t.Helper()
	return command(t, true, "systemctl", args...)
}
func write(t *testing.T, path string, v any) {
	t.Helper()
	if e := os.WriteFile(path, config.Bytes(v), 0600); e != nil {
		t.Fatal(e)
	}
}
func load(t *testing.T, path string) config.Config {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := config.Parse(b, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func state(t *testing.T) *manage.State {
	t.Helper()
	m := manage.New()
	s, e := m.Load(false)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func pid(t *testing.T, unit string) string {
	t.Helper()
	return strings.TrimSpace(string(sys(t, "show", unit, "-p", "MainPID", "--value")))
}
func isolated(t *testing.T) {
	t.Helper()
	if os.Getenv("GOST_DEPLOY_TEST") != "1" || os.Geteuid() != 0 {
		t.Skip("isolated systemd container only")
	}
	if _, e := os.Stat("/.dockerenv"); e != nil {
		t.Fatal("Docker guard")
	}
}
func failingArchive(t *testing.T, dir string) (string, string) {
	t.Helper()
	arch := os.Getenv("GOST_TEST_ARCH")
	if arch == "" {
		arch = "amd64"
	}
	name := "gost_3.99.0_linux_" + arch + ".tar.gz"
	path := filepath.Join(dir, name)
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	script := []byte("#!/bin/sh\nif [ \"$1\" = '-V' ]; then echo 'gost 3.99.0 (linux/" + arch + ")'; exit 0; fi\nif [ \"$3\" = '-O' ]; then echo '{}'; exit 0; fi\nexit 42\n")
	tw.WriteHeader(&tar.Header{Name: "gost", Mode: 0755, Size: int64(len(script))})
	tw.Write(script)
	tw.Close()
	gz.Close()
	f.Close()
	b, _ := os.ReadFile(path)
	hash := sha256.Sum256(b)
	sums := filepath.Join(dir, "checksums.txt")
	os.WriteFile(sums, []byte(hex.EncodeToString(hash[:])+"  "+name+"\n"), 0600)
	return path, sums
}
func TestLifecycle(t *testing.T) {
	isolated(t)
	dir := t.TempDir()
	candidate := filepath.Join(dir, "config.json")
	run(t, "init", "--output", candidate)
	c := load(t, candidate)
	c.Listener.Addr = testkit.Addr(t)
	c.Listener.Users = append(c.Listener.Users, config.Auth{Username: "用户@x", Password: "special :$()'\"\\ 😀"})
	write(t, candidate, c)
	offline := []string{"--archive", "/artifacts/gost_3.3.0_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.3.0.txt"}
	install := append([]string{"install", "--config", candidate}, offline...)
	// Existing resources must remain byte-for-byte intact.
	os.WriteFile("/usr/local/bin/gost", []byte("foreign"), 0755)
	fail(t, install...)
	b, _ := os.ReadFile("/usr/local/bin/gost")
	if string(b) != "foreign" {
		t.Fatal("foreign binary modified")
	}
	os.Remove("/usr/local/bin/gost")
	command(t, true, "useradd", "--system", "--user-group", "gost")
	fail(t, install...)
	command(t, true, "userdel", "gost")
	exec.Command("groupdel", "gost").Run()
	os.WriteFile("/etc/systemd/system/gost.service", []byte("[Service]\nExecStart=/bin/true\n"), 0644)
	fail(t, install...)
	os.Remove("/etc/systemd/system/gost.service")
	sys(t, "daemon-reload")
	t.Log("PASS foreign binary/account/unit conflicts preserved")
	corrupt := filepath.Join(dir, "corrupt.tar.gz")
	os.WriteFile(corrupt, []byte("bad"), 0600)
	fail(t, "install", "--config", candidate, "--archive", corrupt, "--checksums", offline[3])
	if _, e := os.Stat("/etc/systemd/system/gost.service"); !os.IsNotExist(e) {
		t.Fatal("bad archive created unit")
	}
	t.Log("PASS checksum failure before service creation")
	broken, sums := failingArchive(t, dir)
	fail(t, "install", "--config", candidate, "--version", "3.99.0", "--archive", broken, "--checksums", sums)
	for _, p := range []string{"/usr/local/sbin/gostctl", "/etc/systemd/system/gost.service", "/etc/gost/gost.json"} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			t.Fatalf("fresh failure left %s", p)
		}
	}
	if exec.Command("id", "gost").Run() == nil {
		t.Fatal("account retained after fresh failure")
	}
	t.Log("PASS first-install runtime failure removes resources/account")
	run(t, install...)
	initialPID := pid(t, "gost")
	before, _ := os.ReadFile("/var/lib/gost-deploy/state.json")
	run(t, install...)
	after, _ := os.ReadFile("/var/lib/gost-deploy/state.json")
	if string(before) != string(after) || pid(t, "gost") != initialPID {
		t.Fatal("non-idempotent install")
	}
	fail(t, "rollback")
	st, _ := os.Stat("/etc/gost/gost.json")
	if st.Mode().Perm() != 0640 {
		t.Fatal("native permissions")
	}
	st, _ = os.Stat("/etc/gost-deploy/config.json")
	if st.Mode().Perm() != 0600 {
		t.Fatal("manifest permissions")
	}
	command(t, true, "/usr/local/sbin/gostctl", "status")
	command(t, true, "systemd-analyze", "verify", "/etc/systemd/system/gost.service")
	t.Log("PASS install enabled startup/idempotence/independent binary")
	echo := testkit.Echo(t)
	udp := testkit.UDP(t)
	testkit.CheckAuth(t, c)
	_, domainPort, _ := net.SplitHostPort(echo)
	conn := testkit.Connect(t, c, net.JoinHostPort("localhost", domainPort))
	if e := testkit.Payload(conn); e != nil {
		t.Fatal(e)
	}
	conn.Close()
	if e := testkit.UDPRelay(c, udp, false); e != nil {
		t.Fatal(e)
	}
	logs := string(run(t, "logs"))
	for _, a := range c.Listener.Users {
		if strings.Contains(logs, a.Password) {
			t.Fatal("credential in journal")
		}
	}
	t.Log("PASS special credentials/authentication/domain/TCP/UDP disabled")
	for _, verb := range []string{"stop", "start", "restart"} {
		run(t, verb)
	}
	oldPID := pid(t, "gost")
	n := 0
	fmt.Sscan(oldPID, &n)
	syscall.Kill(n, syscall.SIGKILL)
	testkit.Wait(t, 8*time.Second, func() bool { return pid(t, "gost") != "0" && pid(t, "gost") != oldPID && testkit.Ready(c) })
	testkit.CheckAuth(t, c)
	t.Log("PASS lifecycle commands and systemd crash recovery")
	c.Listener.UDP = true
	write(t, candidate, c)
	run(t, "configure", "--config", candidate)
	if e := testkit.UDPRelay(c, udp, true); e != nil {
		t.Fatal(e)
	}
	run(t, "rollback")
	c.Listener.UDP = false
	if e := testkit.UDPRelay(c, udp, false); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS configuration update/rollback and real UDP relay")
	// Real mixed pool updates traverse CLI -> HTTP API -> Unix runtime control.
	nodes := []config.Node{}
	for _, kind := range []string{"http", "socks5"} {
		addr := testkit.Addr(t)
		auth := config.Auth{Username: "up", Password: "up-password"}
		v := native.Service(addr, []config.Auth{auth}, false)
		v["services"].([]any)[0].(native.M)["handler"].(native.M)["type"] = kind
		testkit.Start(t, state(t).Current.Binary, v, addr)
		nodes = append(nodes, config.Node{ID: kind, Type: kind, Addr: addr, Auth: &auth})
	}
	c.Profiles = append(c.Profiles, config.Profile{Name: "mixed", Type: "pool", Nodes: nodes, Probe: &config.Probe{Type: "tcp", Addr: echo, Interval: 30, Timeout: 1}})
	write(t, candidate, c)
	stablePID := pid(t, "gost")
	held := testkit.Connect(t, c, echo)
	defer held.Close()
	run(t, "configure", "--config", candidate)
	run(t, "profile", "use", "mixed")
	if e := testkit.Payload(held); e != nil {
		t.Fatal(e)
	}
	conn = testkit.Connect(t, c, echo)
	if e := testkit.Payload(conn); e != nil {
		t.Fatal(e)
	}
	conn.Close()
	if pid(t, "gost") != stablePID {
		t.Fatal("hot update restarted supervisor")
	}
	run(t, "profile", "probe", "mixed")
	run(t, "profile", "list")
	run(t, "profile", "use", "direct")
	c.ActiveProfile = "direct"
	t.Log("PASS API mixed-pool configure/CLI acknowledged switch preserve live connection/PID")
	bad := c.Clone()
	bad.Listener.Users = nil
	write(t, candidate, bad)
	fail(t, "configure", "--config", candidate)
	if pid(t, "gost") != stablePID {
		t.Fatal("invalid candidate changed service")
	}
	occupied, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	bad = c.Clone()
	bad.Listener.Addr = occupied.Addr().String()
	write(t, candidate, bad)
	fail(t, "configure", "--config", candidate)
	occupied.Close()
	if pid(t, "gost") != stablePID {
		t.Fatal("port conflict changed service")
	}
	t.Log("PASS invalid candidate and occupied port preserve service")
	prior := state(t)
	fail(t, "upgrade", "--version", "3.99.0", "--archive", broken, "--checksums", sums)
	recovered := state(t)
	if recovered.Revision != prior.Revision || recovered.Current != prior.Current {
		t.Fatal("startup failure state diverged")
	}
	testkit.CheckAuth(t, c)
	t.Log("PASS real systemd startup failure restores binary/config/state")
	sys(t, "disable", "gost")
	run(t, "stop")
	c.Listener.UDP = true
	write(t, candidate, c)
	run(t, "configure", "--config", candidate)
	if exec.Command("systemctl", "is-active", "--quiet", "gost").Run() == nil || exec.Command("systemctl", "is-enabled", "--quiet", "gost").Run() == nil {
		t.Fatal("stopped/disabled state lost")
	}
	run(t, "rollback")
	c.Listener.UDP = false
	if exec.Command("systemctl", "is-active", "--quiet", "gost").Run() == nil {
		t.Fatal("rollback started stopped service")
	}
	sys(t, "enable", "gost")
	run(t, "start")
	t.Log("PASS configure/rollback preserve stopped and disabled state")
	run(t, "upgrade", "--version", "3.2.6", "--archive", "/artifacts/gost_3.2.6_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.2.6.txt")
	testkit.CheckAuth(t, c)
	conn = testkit.Connect(t, c, echo)
	conn.Close()
	run(t, "rollback")
	if state(t).Current.Version != "3.3.0" {
		t.Fatal("binary rollback")
	}
	prior = state(t)
	fail(t, "upgrade", "--version", "3.99.1", "--archive", "/missing", "--checksums", "/missing")
	command(t, false, "env", "HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1", "NO_PROXY=", ctl(), "upgrade", "--version", "3.99.1")
	if state(t).Revision != prior.Revision {
		t.Fatal("failed release changed state")
	}
	t.Log("PASS real version transitions/binary rollback and download failure")
	os.MkdirAll("/etc/systemd/system/gost.service.d", 0755)
	drop := "/etc/systemd/system/gost.service.d/foreign.conf"
	os.WriteFile(drop, []byte("[Service]\nEnvironment=EXTERNAL=1\n"), 0644)
	sys(t, "daemon-reload")
	fail(t, "restart")
	b, _ = os.ReadFile(drop)
	if !strings.Contains(string(b), "EXTERNAL=1") {
		t.Fatal("drop-in removed")
	}
	os.Remove(drop)
	os.Remove(filepath.Dir(drop))
	sys(t, "daemon-reload")
	t.Log("PASS external systemd drop-in preserved")
	// Two differently stamped binaries prove manager-only replacement and recoverability.
	write(t, candidate, c)
	apiPID := pid(t, "gost-deploy-api")
	oldManager := state(t).ManagerSHA
	command(t, true, "/fixtures/gostctl-previous", install...)
	if state(t).ManagerSHA == oldManager || pid(t, "gost-deploy-api") == apiPID {
		t.Fatal("manager binary/API not updated")
	}
	testkit.CheckAuth(t, c)
	run(t, install...)
	if state(t).ManagerSHA != oldManager {
		t.Fatal("manager reinstallation did not restore binary")
	}
	// A candidate manager that cannot run must restore the previous executable.
	manager := manage.New()
	brokenManager := filepath.Join(dir, "broken-manager")
	os.WriteFile(brokenManager, []byte("#!/bin/sh\nexit 42\n"), 0755)
	manager.Self = brokenManager
	beforeManager := state(t)
	sys(t, "stop", "gost-deploy-api")
	e = manager.Locked(func() error {
		return manager.Apply(context.Background(), beforeManager, beforeManager.Current, c, nil, true)
	})
	if e == nil || !strings.Contains(e.Error(), "已恢复") {
		t.Fatalf("manager failure did not recover: %v", e)
	}
	if state(t).ManagerSHA != oldManager || state(t).Revision != beforeManager.Revision {
		t.Fatal("manager rollback state diverged")
	}
	testkit.CheckAuth(t, c)
	run(t, "profile", "list")
	t.Log("PASS manager update and rollback")
	verifyOperationsAPI(t)
	run(t, "uninstall")
	if _, e := os.Stat("/etc/gost-deploy/config.json"); e != nil {
		t.Fatal("retained manifest missing")
	}
	run(t, install...)
	run(t, "uninstall", "--purge")
	for _, p := range []string{"/var/lib/gost-deploy", "/opt/gost-deploy", "/usr/local/sbin/gostctl"} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			t.Fatalf("purge left %s", p)
		}
	}
	t.Log("PASS retaining uninstall/reinstall/purge")
	t.Log("PASS lifecycle acceptance")
}
func TestBootPrepare(t *testing.T) {
	isolated(t)
	if os.Getenv("GOST_BOOT") != "prepare" {
		t.Skip("boot prepare only")
	}
	path := "/root/gost-boot.json"
	run(t, "init", "--output", path, "--udp")
	c := load(t, path)
	c.Listener.Addr = "127.0.0.1:1080"
	write(t, path, c)
	command(t, true, "bash", "/src/install.sh", "--config", path, "--archive", "/artifacts/gost_3.3.0_linux_amd64.tar.gz", "--checksums", "/artifacts/checksums-3.3.0.txt")
	t.Log("PASS Bash installer and boot preparation")
}
func TestBootVerify(t *testing.T) {
	isolated(t)
	if os.Getenv("GOST_BOOT") != "verify" {
		t.Skip("boot verify only")
	}
	c := load(t, "/root/gost-boot.json")
	testkit.CheckAuth(t, c)
	echo := testkit.Echo(t)
	conn := testkit.Connect(t, c, echo)
	if e := testkit.Payload(conn); e != nil {
		t.Fatal(e)
	}
	conn.Close()
	if e := testkit.UDPRelay(c, testkit.UDP(t), true); e != nil {
		t.Fatal(e)
	}
	sys(t, "is-enabled", "--quiet", "gost")
	run(t, "uninstall", "--purge")
	os.Remove("/root/gost-boot.json")
	t.Log("PASS boot verification")
}
func TestOpenAPI(t *testing.T) {
	path := filepath.Join(testkit.Root(), "docs/openapi.json")
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	paths := doc["paths"].(map[string]any)
	for _, p := range []string{"/config", "/status", "/profiles", "/nodes", "/active-profile", "/config/validate", "/profiles/{name}/probe", "/service/start", "/service/stop", "/service/restart", "/rollback", "/logs"} {
		if paths[p] == nil {
			t.Fatal("missing OpenAPI operation", p)
		}
	}
}
