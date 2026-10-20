package api

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/auth"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"github.com/ShaohongDong/relayharbor/internal/webui"
)

// rootListener authenticates local CLI peers using the kernel, not request headers.
type rootListener struct{ *net.UnixListener }

func (l rootListener) Accept() (net.Conn, error) {
	for {
		c, e := l.AcceptUnix()
		if e != nil {
			return nil, e
		}
		if rootPeer(c) {
			return c, nil
		}
		c.Close()
	}
}
func Serve(ctx context.Context, m *manage.Manager) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := auth.New(m.AuthStore())
	a.Origin = func(r *http.Request) string {
		if r.TLS != nil {
			return m.AccessOrigin(r.Host, true)
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if p := net.ParseIP(ip); p != nil && p.IsLoopback() && r.Header.Get("X-Forwarded-Proto") == "https" {
			return m.AccessOrigin(r.Host, false)
		}
		return ""
	}
	h := webui.Handler(&Handler{Backend: m, Auth: a})
	tcp, e := net.Listen("tcp", Address)
	if e != nil {
		return e
	}
	defer tcp.Close()
	path := m.APISocket()
	if st, e := os.Lstat(path); e == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("管理 socket 路径被占用")
		}
		if c, e := net.DialTimeout("unix", path, time.Second); e == nil {
			c.Close()
			return errors.New("管理 socket 已在运行")
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	unix, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return e
	}
	defer unix.Close()
	if e = os.Chmod(path, 0600); e != nil {
		return e
	}
	results := make(chan error, 3)
	go func() { results <- ServeListener(ctx, tcp, h) }()
	go func() {
		results <- ServeListener(ctx, rootListener{unix}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, auth.Local(r)) }))
	}()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			_ = m.ReconcileSetup(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	v, e := m.Access()
	if e != nil {
		return e
	}
	if v.Port != 0 && (v.CloseAt == nil || time.Now().Before(*v.CloseAt)) {
		var certMu sync.Mutex
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certMu.Lock()
			defer certMu.Unlock()
			return m.AccessCertificate()
		}}
		if _, e = m.AccessCertificate(); e != nil {
			return e
		}
		public, e := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(v.Port)))
		if e != nil {
			return e
		}
		defer public.Close()
		pubctx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			e := ServeListener(pubctx, tls.NewListener(public, cfg), h)
			if pubctx.Err() == nil {
				results <- e
			}
		}()
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Existing CLI HTTPS setup gets the same handoff as browser setup.
					if m.SetupAddress() != v.URL() {
						_ = m.AccessHandoff()
					}
					access, e := m.Access()
					if e == nil && access.CloseAt != nil && time.Now().After(*access.CloseAt) {
						stop()
						return
					}
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
		return nil
	case e := <-results:
		return e
	}
}
