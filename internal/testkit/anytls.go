package testkit

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitee.com/kenny3755/gost_deploy/internal/socks"
)

func SingBox(t *testing.T, arch, root string) string {
	t.Helper()
	name := "sing-box-1.14.3-linux-" + arch
	raw, e := os.ReadFile(filepath.Join(root, name+".tar.gz"))
	want := map[string]string{"amd64": "e2bdf179c15a3652955dc44867e33ca0965c9c9b91b34267dcc5a5d639a5feee", "arm64": "29dae24d76bea3bc62d07dd3e60cfc12003de7debbd6b768df9be9c68360174e"}[arch]
	h := sha256.Sum256(raw)
	if e != nil || want == "" || hex.EncodeToString(h[:]) != want {
		t.Fatal("sing-box archive checksum", e)
	}
	f, e := os.Open(filepath.Join(root, name+".tar.gz"))
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
		entry, e := tr.Next()
		if e != nil {
			t.Fatal("missing sing-box", e)
		}
		if entry.Name != name+"/sing-box" {
			continue
		}
		if entry.Typeflag != tar.TypeReg {
			t.Fatal("invalid sing-box archive")
		}
		path := filepath.Join(t.TempDir(), "sing-box")
		out, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if e != nil {
			t.Fatal(e)
		}
		_, e = io.Copy(out, tr)
		ce := out.Close()
		if e != nil || ce != nil {
			t.Fatal(e, ce)
		}
		return path
	}
}

func StartSingBox(t *testing.T, binary, path, addr string) {
	t.Helper()
	cmd := Command(context.Background(), binary, "run", "-c", path)
	log, e := os.Create(filepath.Join(t.TempDir(), "sing-box.log"))
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(log.Name())
			t.Log(string(b))
		}
	})
	Wait(t, 8*time.Second, func() bool {
		c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
}

// Mihomo runs the actual pinned client against the exported YAML, with a private
// CA only for isolated acceptance. No insecure TLS option is injected.
func Mihomo(t *testing.T, archive string, yaml []byte, ca string) string {
	t.Helper()
	want := "ba3ce607747a07f948fc35780e108a4a7c7f552a38b9bd4d115f313ebcb89c20"
	arm := strings.Contains(filepath.Base(archive), "arm64")
	if arm {
		want = "9dd862e28b46ff7d775f169cceebc28deccaa0a9e804237d421cd2571e0caba0"
	}
	raw, e := os.ReadFile(archive)
	hash := sha256.Sum256(raw)
	if e != nil || hex.EncodeToString(hash[:]) != want {
		t.Fatal("mihomo archive checksum", e)
	}
	dir := t.TempDir()
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
	bin := filepath.Join(dir, "mihomo")
	out, e := os.OpenFile(bin, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(out, gz)
	ce := out.Close()
	if e != nil || ce != nil {
		t.Fatal(e, ce)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	l.Close()
	_, port, _ := net.SplitHostPort(addr)
	_, e = strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "config.yaml")
	data := strings.Replace(string(yaml), "mixed-port: 7890", "mixed-port: "+port, 1)
	// The isolated ACME domain is intentionally absent from public DNS.
	// Mihomo's resolver has its own hosts map, separate from container /etc/hosts.
	data += "\nhosts:\n  gost-panel.test: 127.0.0.1\n"
	if e = os.WriteFile(path, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"-d", dir, "-f", path}
	if arm && os.Getenv("GOST_TEST_QEMU") != "" {
		args = append([]string{bin}, args...)
		bin = os.Getenv("GOST_TEST_QEMU")
	}
	cmd := exec.Command(bin, args...)
	if ca != "" {
		cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+ca)
	}
	logPath := filepath.Join(dir, "client.log")
	log, e := os.Create(logPath)
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Log(string(b))
		}
	})
	for i := 0; i < 100; i++ {
		c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if e == nil {
			c.Close()
			return addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mihomo failed to listen")
	return ""
}

func ClientConnect(addr, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, e := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if e != nil {
		return nil, e
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, e = c.Write([]byte{5, 1, 0}); e != nil {
		c.Close()
		return nil, e
	}
	reply := make([]byte, 2)
	if _, e = io.ReadFull(c, reply); e != nil {
		c.Close()
		return nil, e
	}
	a := socks.Target(target)
	if _, e = c.Write(append([]byte{5, 1, 0}, a...)); e != nil {
		c.Close()
		return nil, e
	}
	head := make([]byte, 3)
	if _, e = io.ReadFull(c, head); e != nil {
		c.Close()
		return nil, e
	}
	if head[1] != 0 {
		c.Close()
		return nil, os.ErrPermission
	}
	// Bound-address length varies; consume the RFC1928 address before payload.
	typ := make([]byte, 1)
	if _, e = io.ReadFull(c, typ); e != nil {
		c.Close()
		return nil, e
	}
	n := 0
	switch typ[0] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		if _, e = io.ReadFull(c, typ); e != nil {
			c.Close()
			return nil, e
		}
		n = int(typ[0])
	default:
		c.Close()
		return nil, os.ErrInvalid
	}
	if _, e = io.ReadFull(c, make([]byte, n+2)); e != nil {
		c.Close()
		return nil, e
	}
	c.SetDeadline(time.Time{})
	return c, nil
}
