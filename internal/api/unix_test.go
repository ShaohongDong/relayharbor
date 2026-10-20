package api

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientRejectsUnprivilegedSocketBeforeSendingSecrets(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged peer; real root transport is covered in systemd acceptance")
	}
	path := filepath.Join(t.TempDir(), "spoof.sock")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	received := make(chan int, 1)
	go func() {
		c, e := l.Accept()
		if e != nil {
			received <- -1
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 1024)
		n, _ := c.Read(b)
		received <- n
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, e = Client(ctx, path, "PUT", "/config", map[string]string{"password": "must-never-reach-peer"}, "revision"); e == nil {
		t.Fatal("unprivileged server accepted")
	}
	if n := <-received; n != 0 {
		t.Fatalf("sent %d bytes before authenticating peer", n)
	}
}
