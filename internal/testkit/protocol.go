// Package testkit contains only loopback test helpers; it is never linked into gostctl.
package testkit

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/socks"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
func Command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	if q := os.Getenv("GOST_TEST_QEMU"); q != "" {
		return exec.CommandContext(ctx, q, append([]string{binary}, args...)...)
	}
	return exec.CommandContext(ctx, binary, args...)
}
func GOST(t *testing.T) string {
	t.Helper()
	arch := os.Getenv("GOST_TEST_ARCH")
	if arch == "" {
		arch = runtime.GOARCH
	}
	archive := os.Getenv("GOST_ARCHIVE")
	if archive == "" {
		archive = filepath.Join(Root(), ".cache/releases/gost_3.3.0_linux_"+arch+".tar.gz")
	}
	b, e := os.ReadFile(archive)
	if os.IsNotExist(e) && os.Getenv("GOST_TEST_REAL") != "1" {
		t.Skip("real GOST requires make fetch")
	} else if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(b)
	pins := map[string]string{"amd64": "676fb7f78d267b6ae73df719c0c7f2b565dde7147da935cfafbc1e1da558b6d5", "arm64": "d03699e3f385d4ff5dad68046712adfcc7515325a064d2ab046e0bece30f8f8f"}
	if hex.EncodeToString(hash[:]) != pins[arch] {
		t.Fatal("GOST archive checksum mismatch")
	}
	f, e := os.Open(archive)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, e := tr.Next()
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == "gost" {
			path := filepath.Join(t.TempDir(), "gost")
			out, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0755)
			if e != nil {
				t.Fatal(e)
			}
			_, e = io.Copy(out, tr)
			out.Close()
			if e != nil {
				t.Fatal(e)
			}
			return path
		}
	}
}
func Addr(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return l.Addr().String()
}
func Echo(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); l.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				stop := context.AfterFunc(ctx, func() { c.Close() })
				defer stop()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return l.Addr().String()
}
func UDP(t *testing.T) string {
	t.Helper()
	c, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		b := make([]byte, 65536)
		for {
			n, addr, e := c.ReadFrom(b)
			if e != nil {
				return
			}
			_, _ = c.WriteTo(b[:n], addr)
		}
	}()
	return c.LocalAddr().String()
}
func Start(t *testing.T, binary string, v any, addr string) *exec.Cmd {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gost.json")
	if e := os.WriteFile(path, config.Bytes(v), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := Command(context.Background(), binary, "-C", path)
	log, e := os.Create(filepath.Join(t.TempDir(), "gost.log"))
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); log.Close(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	Wait(t, 5*time.Second, func() bool {
		c, e := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	return cmd
}
func Wait(t *testing.T, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func Connect(t *testing.T, c config.Config, target string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, e := socks.Dial(ctx, Local(c.Listener.Addr), c.Listener.Users[0], socks.Connect, socks.Target(target))
	if e != nil {
		t.Fatal(e)
	}
	return conn
}
func Local(addr string) string {
	h, p, _ := net.SplitHostPort(addr)
	if h == "0.0.0.0" {
		h = "127.0.0.1"
	}
	if h == "::" {
		h = "::1"
	}
	return net.JoinHostPort(h, p)
}
func Payload(c net.Conn) error {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.SetDeadline(time.Time{})
	p := []byte("still-alive-through-profile-switch")
	if _, e := c.Write(p); e != nil {
		return e
	}
	b := make([]byte, len(p))
	if _, e := io.ReadFull(c, b); e != nil {
		return e
	}
	if string(b) != string(p) {
		return fmt.Errorf("echo mismatch")
	}
	return nil
}
func JSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func CheckAuth(t *testing.T, c config.Config) {
	t.Helper()
	for _, a := range c.Listener.Users {
		conn, e := net.DialTimeout("tcp", Local(c.Listener.Addr), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		if e = socks.Authenticate(conn, a); e != nil {
			t.Fatal(e)
		}
		conn.Close()
	}
	for _, method := range []byte{0, 2} {
		conn, e := net.DialTimeout("tcp", Local(c.Listener.Addr), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		if method == 0 {
			conn.Write([]byte{5, 1, 0})
			b := make([]byte, 2)
			io.ReadFull(conn, b)
			if string(b) == "\x05\x00" {
				t.Fatal("anonymous allowed")
			}
		} else {
			if socks.Authenticate(conn, config.Auth{Username: "wrong", Password: "wrong"}) == nil {
				t.Fatal("wrong credentials allowed")
			}
		}
		conn.Close()
	}
}
func Ready(c config.Config) bool {
	conn, e := net.DialTimeout("tcp", Local(c.Listener.Addr), 200*time.Millisecond)
	if e != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	return socks.Authenticate(conn, c.Listener.Users[0]) == nil
}

func UDPRelay(c config.Config, target string, want bool) error {
	conn, e := net.DialTimeout("tcp", Local(c.Listener.Addr), time.Second)
	if e != nil {
		return e
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	if e = socks.Authenticate(conn, c.Listener.Users[0]); e != nil {
		return e
	}
	reply, e := socks.Request(conn, 3, socks.Address("0.0.0.0", 0))
	if e != nil {
		if !want {
			return nil
		}
		return e
	}
	if len(reply) != 10 || reply[3] != 1 {
		return fmt.Errorf("unexpected UDP reply")
	}
	host := net.IP(reply[4:8]).String()
	if host == "0.0.0.0" {
		h, _, _ := net.SplitHostPort(Local(c.Listener.Addr))
		host = h
	}
	port := int(reply[8])<<8 | int(reply[9])
	udp, e := net.DialTimeout("udp", net.JoinHostPort(host, fmt.Sprint(port)), time.Second)
	if e != nil {
		return e
	}
	defer udp.Close()
	udp.SetDeadline(time.Now().Add(2 * time.Second))
	packet := append([]byte{0, 0, 0}, socks.Target(target)...)
	packet = append(packet, []byte("udp-preserved")...)
	if _, e = udp.Write(packet); e != nil {
		return e
	}
	b := make([]byte, 512)
	n, e := udp.Read(b)
	good := e == nil && strings.HasSuffix(string(b[:n]), "udp-preserved")
	if good != want {
		return fmt.Errorf("UDP enabled=%v, received=%v", want, good)
	}
	return nil
}
