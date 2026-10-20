package manage

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/subscription"
)

var ErrAnyTLSRevision = errors.New("AnyTLS 配置已变化，请重新读取后更新")
var ErrSubscription = errors.New("订阅不存在")

type AnyTLSState struct {
	Schema   int                 `json:"schema"`
	Revision string              `json:"revision"`
	Enabled  bool                `json:"enabled"`
	Domain   string              `json:"domain"`
	Release  Release             `json:"release"`
	Users    []subscription.User `json:"users"`
}

type AnyTLSUserStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
	Active  bool   `json:"active"`
}
type AnyTLSStatus struct {
	Configured bool               `json:"configured"`
	Enabled    bool               `json:"enabled"`
	Domain     string             `json:"domain,omitempty"`
	Version    string             `json:"version,omitempty"`
	Users      []AnyTLSUserStatus `json:"users"`
}
type AnyTLSRequest struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Port int    `json:"port,omitempty"`
}
type AnyTLSCredentials struct {
	Password string `json:"password"`
	URL      string `json:"url"`
	Provider string `json:"provider"`
}
type anyTLSLaunch struct {
	Release Release `json:"release"`
	SHA256  string  `json:"sha256"`
}

func (m *Manager) anyTLSPath() string {
	return filepath.Join(filepath.Dir(m.P.State), "anytls.json")
}
func (m *Manager) anyTLSRoot() string {
	return filepath.Join(filepath.Dir(m.P.Input), "anytls")
}
func (m *Manager) anyTLSDir(id string) string { return filepath.Join(m.anyTLSRoot(), id) }
func anyTLSUnit(id string) string             { return "gost-deploy-anytls-" + id + ".service" }
func (m *Manager) anyTLSUnitPath(id string) string {
	return filepath.Join(filepath.Dir(m.P.Unit), anyTLSUnit(id))
}
func anyTLSUnitText(id string) string {
	return fmt.Sprintf(`[Unit]
Description=GOST AnyTLS client %s
Wants=network-online.target
After=network-online.target gost.service
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
User=gost
Group=gost
ExecStart=/usr/local/sbin/gostctl _anytls %s
Restart=on-failure
RestartSec=2
TimeoutStopSec=10
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
CapabilityBoundingSet=
AmbientCapabilities=
StandardOutput=null
StandardError=journal

[Install]
WantedBy=multi-user.target
`, id, id)
}

func (m *Manager) loadAnyTLS() (*AnyTLSState, error) {
	b, e := os.ReadFile(m.anyTLSPath())
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var s AnyTLSState
	if e = decodeState(b, &s); e != nil {
		return nil, e
	}
	if s.Schema != 1 || s.Revision == "" || len(s.Users) > 128 {
		return nil, errors.New("AnyTLS 状态无效")
	}
	if e = ValidateTLSOptions(TLSOptions{Domain: s.Domain, Email: "validate@example.com", AcceptTOS: true}); e != nil {
		return nil, errors.New("AnyTLS 域名无效")
	}
	ids, ports, tokens, passwords := map[string]bool{}, map[int]bool{}, map[string]bool{}, map[string]bool{}
	for _, u := range s.Users {
		if u.Validate() != nil || ids[u.ID] || ports[u.Port] || tokens[u.Token] || passwords[u.Password] {
			return nil, errors.New("AnyTLS 用户状态无效")
		}
		ids[u.ID], ports[u.Port], tokens[u.Token], passwords[u.Password] = true, true, true, true
	}
	return &s, nil
}

// Callers hold the lifecycle lock before the TLS lock. Renewal holds only the
// TLS lock and never acquires the lifecycle lock, avoiding inverted acquisition.
func (m *Manager) AnyTLS(ctx context.Context, op string, req AnyTLSRequest, expected string) (any, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	var data any
	rev := "unconfigured"
	e := m.Locked(func() error {
		return m.tlsLocked(func() error {
			core, e := m.Load(false)
			if e != nil {
				return e
			}
			if e = m.Managed(ctx, core); e != nil {
				return e
			}
			if op != "status" && op != "credentials" {
				if busy, e := m.tlsOperation(ctx); e != nil {
					return e
				} else if busy != "" {
					return ErrTLSBusy
				}
			}
			s, e := m.loadAnyTLS()
			if e != nil {
				return e
			}
			if s != nil {
				rev = s.Revision
			}
			if op == "status" {
				status := AnyTLSStatus{Users: []AnyTLSUserStatus{}}
				if s != nil {
					if e = m.verifyAnyTLS(ctx, s); e != nil {
						return e
					}
					status.Configured, status.Enabled, status.Domain, status.Version = true, s.Enabled, s.Domain, s.Release.Version
					for _, u := range s.Users {
						_, ae := m.sys(ctx, "is-active", "--quiet", anyTLSUnit(u.ID))
						status.Users = append(status.Users, AnyTLSUserStatus{u.ID, u.Name, u.Port, u.Enabled, ae == nil})
					}
				}
				data = status
				return nil
			}
			if expected == "" || expected != rev {
				return ErrAnyTLSRevision
			}
			if s == nil {
				return errors.New("请先执行 gostctl anytls setup")
			}
			if e = m.verifyAnyTLS(ctx, s); e != nil {
				return e
			}
			if op == "credentials" {
				for _, u := range s.Users {
					if u.ID == req.ID && s.Enabled && u.Enabled {
						data = AnyTLSCredentials{u.Password, "https://" + s.Domain + "/sub/" + u.Token + "/clash.yaml", "https://" + s.Domain + "/sub/" + u.Token + "/proxies.yaml"}
						return nil
					}
				}
				return ErrSubscription
			}
			if e = m.changeAnyTLS(ctx, core, s, op, req); e != nil {
				return e
			}
			rev = s.Revision
			data = map[string]string{"state": "applied"}
			return nil
		})
	})
	return data, rev, e
}

func (m *Manager) verifyAnyTLS(ctx context.Context, s *AnyTLSState) error {
	if e := m.verifySingBox(s.Release); e != nil {
		return e
	}
	for _, u := range s.Users {
		p := m.anyTLSUnitPath(u.ID)
		real, e := filepath.EvalSymlinks(p)
		if e != nil || real != p {
			return errors.New("AnyTLS unit 路径不符")
		}
		b, e := os.ReadFile(p)
		if e != nil || string(b) != anyTLSUnitText(u.ID) {
			return errors.New("AnyTLS unit 被外部修改")
		}
		b, e = m.sys(ctx, "show", anyTLSUnit(u.ID), "-p", "FragmentPath", "--value")
		if e != nil || strings.TrimSpace(string(b)) != p {
			return errors.New("AnyTLS 实际 unit 路径不符")
		}
		b, e = m.sys(ctx, "show", anyTLSUnit(u.ID), "-p", "DropInPaths", "--value")
		if e != nil || strings.TrimSpace(string(b)) != "" {
			return errors.New("发现外部 AnyTLS drop-in")
		}
		launch, e := m.readAnyTLSLaunchConfig(u.ID)
		if e != nil {
			return e
		}
		if launch.Release != s.Release {
			return errors.New("AnyTLS 实例内核与受管版本不符")
		}
	}
	return nil
}

func (m *Manager) AnyTLSSetup(ctx context.Context, archive string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return m.Locked(func() error {
		return m.tlsLocked(func() error {
			core, e := m.Load(false)
			if e != nil {
				return e
			}
			if e = m.Managed(ctx, core); e != nil {
				return e
			}
			if busy, e := m.tlsOperation(ctx); e != nil {
				return e
			} else if busy != "" {
				return ErrTLSBusy
			}
			t, e := m.loadTLS()
			if e != nil {
				return e
			}
			if t == nil || !t.Enabled {
				return errors.New("请先配置并启用 Nginx HTTPS")
			}
			if e = m.verifyTLS(t); e != nil {
				return e
			}
			// Upgrade the owned site before publishing any subscription secrets.
			oldSite, e := capture(m.tlsPaths().site)
			if e != nil {
				return e
			}
			oldTLS, e := capture(m.tlsPaths().config)
			if e != nil {
				return e
			}
			if e = m.writeTLSSite(t, m.tlsSite(t, true, "")); e != nil {
				return e
			}
			if e = m.nginxReload(ctx); e != nil {
				recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return errors.Join(e, m.restore([]fileSnapshot{oldSite, oldTLS}), m.nginxReload(recovery))
			}
			s, e := m.loadAnyTLS()
			if e != nil {
				return e
			}
			if s != nil {
				if e = m.verifyAnyTLS(ctx, s); e != nil {
					return e
				}
				return m.changeAnyTLS(ctx, core, s, "enable", AnyTLSRequest{})
			}
			// Existing unknown files and units must not be adopted.
			matches, e := filepath.Glob(filepath.Join(filepath.Dir(m.P.Unit), "gost-deploy-anytls-*"))
			if e != nil || len(matches) > 0 || exists(m.anyTLSRoot()) {
				return errors.New("发现已有 AnyTLS 资源，拒绝接管")
			}
			r, e := m.prepareSingBox(ctx, archive)
			if e != nil {
				return e
			}
			s = &AnyTLSState{Schema: 1, Revision: config.Secret(), Enabled: true, Domain: t.Domain, Release: r, Users: []subscription.User{}}
			return writeJSON(m.anyTLSPath(), s, 0600, -1)
		})
	})
}

func (m *Manager) anyTLSFiles(id string) []string {
	d := m.anyTLSDir(id)
	return []string{m.anyTLSUnitPath(id), filepath.Join(d, "config.json"), filepath.Join(d, "launch.json"), filepath.Join(d, "cert.pem"), filepath.Join(d, "key.pem")}
}
func (m *Manager) anyTLSPrivateDir(path string) error {
	gid, e := m.Group()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(path, 0750); e != nil {
		return e
	}
	if real, e := filepath.EvalSymlinks(path); e != nil || real != path {
		return errors.New("AnyTLS 目录包含符号链接")
	}
	if e = os.Chmod(path, 0750); e != nil {
		return e
	}
	return os.Chown(path, os.Geteuid(), gid)
}
func (m *Manager) writeAnyTLSUser(core *State, s *AnyTLSState, u subscription.User) error {
	gid, e := m.Group()
	if e != nil {
		return e
	}
	for _, p := range []string{m.anyTLSRoot(), m.anyTLSDir(u.ID)} {
		if e = m.anyTLSPrivateDir(p); e != nil {
			return e
		}
	}
	d := m.anyTLSDir(u.ID)
	b := config.Bytes(subscription.Native(u, core.Runtime.Gateway, core.Runtime.Auth, filepath.Join(d, "cert.pem"), filepath.Join(d, "key.pem")))
	if e = Atomic(filepath.Join(d, "config.json"), b, 0640, gid); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(d, "launch.json"), anyTLSLaunch{s.Release, hashText(b)}, 0640, gid); e != nil {
		return e
	}
	if e = Atomic(m.anyTLSUnitPath(u.ID), []byte(anyTLSUnitText(u.ID)), 0644, -1); e != nil {
		return e
	}
	return m.copyAnyTLSCert(u.ID)
}
func (m *Manager) copyAnyTLSCert(id string) error {
	gid, e := m.Group()
	if e != nil {
		return e
	}
	t, e := m.loadTLS()
	if e != nil || t == nil {
		return errors.New("缺少 HTTPS 证书配置")
	}
	cert, e := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/fullchain.pem"))
	if e != nil {
		return e
	}
	key, e := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/privkey.pem"))
	if e != nil {
		return e
	}
	if _, e = certificate(cert, key, t.Domain); e != nil {
		return e
	}
	for _, v := range []struct {
		name string
		data []byte
	}{{"cert.pem", cert}, {"key.pem", key}} {
		if e = Atomic(filepath.Join(m.anyTLSDir(id), v.name), v.data, 0640, gid); e != nil {
			return e
		}
	}
	return nil
}

func (m *Manager) readAnyTLSLaunch(id string) (anyTLSLaunch, error) {
	l, e := m.readAnyTLSLaunchConfig(id)
	if e != nil {
		return l, e
	}
	return l, m.verifySingBox(l.Release)
}

func (m *Manager) readAnyTLSLaunchConfig(id string) (anyTLSLaunch, error) {
	var l anyTLSLaunch
	if !config.Identifier(id) {
		return l, config.ErrInvalid
	}
	d := m.anyTLSDir(id)
	for _, p := range []string{filepath.Join(d, "launch.json"), filepath.Join(d, "config.json")} {
		real, e := filepath.EvalSymlinks(p)
		if e != nil || real != p {
			return l, errors.New("AnyTLS 运行配置路径不符")
		}
	}
	b, e := os.ReadFile(filepath.Join(d, "launch.json"))
	if e != nil {
		return l, e
	}
	if e = decodeState(b, &l); e != nil {
		return l, e
	}
	h, e := Digest(filepath.Join(d, "config.json"))
	if e != nil || h != l.SHA256 {
		return l, errors.New("AnyTLS 运行配置校验失败")
	}
	return l, nil
}

func (m *Manager) AnyTLSExec(id string) error {
	l, e := m.readAnyTLSLaunch(id)
	if e != nil {
		return e
	}
	return syscall.Exec(l.Release.Binary, []string{l.Release.Binary, "run", "-c", filepath.Join(m.anyTLSDir(id), "config.json")}, os.Environ())
}

func (m *Manager) anyTLSReady(ctx context.Context, domain string, u subscription.User) error {
	if m.AnyTLSProbe != nil {
		return m.AnyTLSProbe(ctx, domain, u.Port)
	}
	cert, e := os.ReadFile(filepath.Join(m.anyTLSDir(u.ID), "cert.pem"))
	if e != nil {
		return e
	}
	key, e := os.ReadFile(filepath.Join(m.anyTLSDir(u.ID), "key.pem"))
	if e != nil {
		return e
	}
	leaf, e := certificate(cert, key, domain)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	for {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 300 * time.Millisecond}, Config: &tls.Config{
			ServerName: domain, MinVersion: tls.VersionTLS12,
			// Pin the already validated local leaf; local ACME acceptance uses a private CA.
			InsecureSkipVerify: true,
			VerifyConnection: func(c tls.ConnectionState) error {
				if len(c.PeerCertificates) == 0 || !c.PeerCertificates[0].Equal(leaf) {
					return errors.New("AnyTLS 尚未加载目标证书")
				}
				return nil
			},
		}}
		probe, pcancel := context.WithTimeout(ctx, time.Second)
		c, e := d.DialContext(probe, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(u.Port)))
		pcancel()
		if e == nil {
			c.Close()
			if _, e := m.sys(ctx, "is-active", "--quiet", anyTLSUnit(u.ID)); e == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("AnyTLS 实例未通过 TLS 启动/重载检查")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func availableAnyTLSPort(port int) bool {
	l, e := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if e != nil {
		return false
	}
	l.Close()
	return true
}

// changeAnyTLS changes only affected instances. Every durable change completes
// before acknowledging its new revision; failures restore the previous files.
func (m *Manager) changeAnyTLS(ctx context.Context, core *State, s *AnyTLSState, op string, req AnyTLSRequest) (err error) {
	oldBytes := config.Bytes(s)
	var old AnyTLSState
	_ = json.Unmarshal(oldBytes, &old)
	var next AnyTLSState
	_ = json.Unmarshal(oldBytes, &next)
	original := s
	s = &next
	defer func() {
		if err == nil {
			*original = *s
		}
	}()
	index := -1
	for i, u := range s.Users {
		if u.ID == req.ID {
			index = i
		}
	}
	affected := []string{}
	restart := false
	switch op {
	case "enable":
		t, e := m.loadTLS()
		if e != nil || t == nil || !t.Enabled {
			return errors.New("需要已启用的 HTTPS")
		}
		s.Enabled, s.Domain = true, t.Domain
		for _, u := range s.Users {
			affected = append(affected, u.ID)
		}
	case "disable":
		s.Enabled = false
		for _, u := range s.Users {
			affected = append(affected, u.ID)
		}
	case "add":
		if !s.Enabled || len(s.Users) >= 128 || !config.Identifier(req.ID) || !config.Text(req.Name, 128) || index >= 0 {
			return errors.New("用户 ID/名称无效、重复、达到 128 用户上限或 AnyTLS 未启用")
		}
		used := map[int]bool{}
		for _, u := range s.Users {
			used[u.Port] = true
		}
		// A stopped managed listener still owns its configured port.
		managed, e := m.ReadConfig()
		if e != nil {
			return e
		}
		for _, addr := range []string{managed.Listener.Addr, core.Runtime.Gateway, "127.0.0.1:18080"} {
			_, p, e := net.SplitHostPort(addr)
			if e != nil {
				return e
			}
			port, _ := strconv.Atoi(p)
			used[port] = true
		}
		port := req.Port
		if port == 0 {
			for port = 8443; port <= 65535; port++ {
				if !used[port] && availableAnyTLSPort(port) {
					break
				}
			}
		}
		if port < 1024 || port > 65535 || used[port] || !availableAnyTLSPort(port) {
			return errors.New("AnyTLS 端口不可用")
		}
		if exists(m.anyTLSDir(req.ID)) || exists(m.anyTLSUnitPath(req.ID)) || exists(m.anyTLSUnitPath(req.ID)+".d") {
			return errors.New("发现外部用户资源，拒绝接管")
		}
		load, e := m.sys(ctx, "show", anyTLSUnit(req.ID), "-p", "LoadState", "--value")
		if e != nil || strings.TrimSpace(string(load)) != "not-found" {
			return errors.New("发现同名 AnyTLS 服务")
		}
		s.Users = append(s.Users, subscription.User{ID: req.ID, Name: req.Name, Port: port, Enabled: true, Password: config.Secret(), Token: config.Secret()})
		affected = append(affected, req.ID)
	default:
		if index < 0 {
			return errors.New("AnyTLS 用户不存在")
		}
		u := &s.Users[index]
		switch op {
		case "rename":
			if !config.Text(req.Name, 128) {
				return config.ErrInvalid
			}
			u.Name = req.Name
		case "user-enable":
			if !s.Enabled {
				return errors.New("请先启用 AnyTLS")
			}
			u.Enabled = true
			affected = append(affected, u.ID)
		case "user-disable":
			u.Enabled = false
			affected = append(affected, u.ID)
		case "delete":
			affected = append(affected, u.ID)
			s.Users = append(s.Users[:index], s.Users[index+1:]...)
		case "reset-token":
			u.Token = config.Secret()
		case "reset-password":
			u.Password = config.Secret()
			affected = append(affected, u.ID)
			restart = true
		default:
			return errors.New("未知 AnyTLS 操作")
		}
	}
	snapshots := []fileSnapshot{}
	wasActive, wasEnabled := map[string]bool{}, map[string]bool{}
	for _, id := range affected {
		_, e := m.sys(ctx, "is-active", "--quiet", anyTLSUnit(id))
		wasActive[id] = e == nil
		_, e = m.sys(ctx, "is-enabled", "--quiet", anyTLSUnit(id))
		wasEnabled[id] = e == nil
		for _, p := range m.anyTLSFiles(id) {
			v, e := capture(p)
			if e != nil {
				return e
			}
			snapshots = append(snapshots, v)
		}
	}
	for _, u := range s.Users {
		if _, affectedUser := wasActive[u.ID]; affectedUser && s.Enabled && u.Enabled && !wasActive[u.ID] && !availableAnyTLSPort(u.Port) {
			return errors.New("AnyTLS 用户端口已被其他服务占用")
		}
	}
	defer func() {
		if err == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, id := range affected {
			_, _ = m.sys(recovery, "stop", anyTLSUnit(id))
		}
		gid, ge := m.Group()
		err = errors.Join(err, ge)
		for _, f := range snapshots {
			if !f.present {
				if e := os.Remove(f.path); e != nil && !os.IsNotExist(e) {
					err = errors.Join(err, e)
				}
			} else {
				g := -1
				if within(f.path, m.anyTLSRoot()) {
					g = gid
					err = errors.Join(err, m.anyTLSPrivateDir(filepath.Dir(f.path)))
				}
				err = errors.Join(err, Atomic(f.path, f.data, f.mode, g))
			}
		}
		_, e := m.sys(recovery, "daemon-reload")
		err = errors.Join(err, e)
		for _, id := range affected {
			verb := "disable"
			if wasEnabled[id] {
				verb = "enable"
			}
			_, e = m.sys(recovery, verb, anyTLSUnit(id))
			err = errors.Join(err, e)
			if wasActive[id] {
				_, e = m.sys(recovery, "start", anyTLSUnit(id))
				err = errors.Join(err, e)
			}
			if op == "add" {
				_ = os.Remove(m.anyTLSDir(id))
			}
		}
		*s = old
		err = errors.Join(err, Atomic(m.anyTLSPath(), oldBytes, 0600, -1))
	}()
	for _, id := range affected {
		var user *subscription.User
		for i := range s.Users {
			if s.Users[i].ID == id {
				user = &s.Users[i]
			}
		}
		if user != nil && op != "disable" && op != "user-disable" {
			if err = m.writeAnyTLSUser(core, s, *user); err != nil {
				return err
			}
			if _, err = m.Exec(ctx, s.Release.Binary, "check", "-c", filepath.Join(m.anyTLSDir(id), "config.json")); err != nil {
				return errors.New("AnyTLS 候选配置检查失败")
			}
		}
		if _, err = m.sys(ctx, "daemon-reload"); err != nil {
			return err
		}
		if user == nil || !s.Enabled || !user.Enabled {
			if _, err = m.sys(ctx, "disable", "--now", anyTLSUnit(id)); err != nil {
				return err
			}
			if user == nil {
				for _, p := range m.anyTLSFiles(id) {
					if err = os.Remove(p); err != nil && !os.IsNotExist(err) {
						return err
					}
				}
				_ = os.Remove(m.anyTLSDir(id))
				if _, err = m.sys(ctx, "daemon-reload"); err != nil {
					return err
				}
			}
		} else {
			if _, err = m.sys(ctx, "enable", anyTLSUnit(id)); err != nil {
				return err
			}
			verb := "start"
			if restart {
				verb = "restart"
			}
			if _, err = m.sys(ctx, verb, anyTLSUnit(id)); err != nil {
				return err
			}
			if err = m.anyTLSReady(ctx, s.Domain, *user); err != nil {
				return err
			}
		}
	}
	s.Revision = config.Secret()
	return writeJSON(m.anyTLSPath(), s, 0600, -1)
}

func (m *Manager) Subscription(ctx context.Context, token string, provider bool) ([]byte, error) {
	var data []byte
	e := m.Locked(func() error {
		return m.tlsLocked(func() error {
			s, e := m.loadAnyTLS()
			if e != nil {
				return e
			}
			t, e := m.loadTLS()
			if e != nil {
				return e
			}
			if s == nil || !s.Enabled || t == nil || !t.Enabled || t.Domain != s.Domain {
				return ErrSubscription
			}
			for _, u := range s.Users {
				if subtle.ConstantTimeCompare([]byte(u.Token), []byte(token)) == 1 && u.Enabled {
					data = subscription.YAML(s.Domain, u, provider)
					return nil
				}
			}
			return ErrSubscription
		})
	})
	return data, e
}

// Called with the TLS lock held. Atomic file replacement preserves running
// sessions; sing-box retains its previous in-memory key pair until both file
// watchers have loaded a matching new pair.
func (m *Manager) deployAnyTLSCert(ctx context.Context) (err error) {
	s, e := m.loadAnyTLS()
	if e != nil || s == nil {
		return e
	}
	if e = m.verifyAnyTLS(ctx, s); e != nil {
		return e
	}
	old := []fileSnapshot{}
	for _, u := range s.Users {
		for _, name := range []string{"cert.pem", "key.pem"} {
			f, e := capture(filepath.Join(m.anyTLSDir(u.ID), name))
			if e != nil {
				return e
			}
			old = append(old, f)
		}
	}
	defer func() {
		if err == nil {
			return
		}
		gid, e := m.Group()
		err = errors.Join(err, e)
		for _, f := range old {
			err = errors.Join(err, Atomic(f.path, f.data, f.mode, gid))
		}
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, u := range s.Users {
			if _, e := m.sys(recovery, "is-active", "--quiet", anyTLSUnit(u.ID)); e == nil {
				err = errors.Join(err, m.anyTLSReady(recovery, s.Domain, u))
			}
		}
	}()
	for _, u := range s.Users {
		if err = m.copyAnyTLSCert(u.ID); err != nil {
			return err
		}
		if _, e := m.sys(ctx, "is-active", "--quiet", anyTLSUnit(u.ID)); e == nil {
			if err = m.anyTLSReady(ctx, s.Domain, u); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) guardAnyTLSTLS(domain string) error {
	s, e := m.loadAnyTLS()
	if e != nil {
		return e
	}
	if s != nil && s.Enabled && domain != s.Domain {
		return errors.New("请先停用 AnyTLS，再停用 HTTPS 或修改域名")
	}
	return nil
}

func (m *Manager) uninstallAnyTLS(ctx context.Context, core *State, purge bool) error {
	s, e := m.loadAnyTLS()
	if e != nil || s == nil {
		return e
	}
	if e = m.verifyAnyTLS(ctx, s); e != nil {
		return e
	}
	if e = m.changeAnyTLS(ctx, core, s, "disable", AnyTLSRequest{}); e != nil {
		return e
	}
	if !purge {
		return nil
	}
	for _, u := range s.Users {
		if e = os.Remove(m.anyTLSUnitPath(u.ID)); e != nil {
			return e
		}
	}
	if _, e = m.sys(ctx, "daemon-reload"); e != nil {
		return e
	}
	if e = os.RemoveAll(m.anyTLSRoot()); e != nil {
		return e
	}
	return os.Remove(m.anyTLSPath())
}
