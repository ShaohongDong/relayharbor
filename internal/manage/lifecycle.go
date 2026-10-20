package manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gitee.com/kenny3755/gost_deploy/internal/config"
	"gitee.com/kenny3755/gost_deploy/internal/native"
	"gitee.com/kenny3755/gost_deploy/internal/socks"
	"gitee.com/kenny3755/gost_deploy/internal/supervisor"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const StateSchema = 3

type Snapshot struct {
	Release Release       `json:"release"`
	Config  config.Config `json:"config"`
	Active  bool          `json:"active"`
	Enabled bool          `json:"enabled"`
}
type State struct {
	Schema      int            `json:"schema"`
	Installed   bool           `json:"installed"`
	OwnsAccount bool           `json:"owns_account"`
	Current     Release        `json:"current"`
	Previous    *Snapshot      `json:"previous"`
	Revision    string         `json:"revision"`
	Runtime     native.Options `json:"runtime"`
	Manager     string         `json:"manager"`
	ManagerSHA  string         `json:"managerSha256"`
}

func (m *Manager) Load(optional bool) (*State, error) {
	b, e := os.ReadFile(m.P.State)
	if os.IsNotExist(e) && optional {
		return nil, nil
	}
	if e != nil {
		return nil, errors.New("无法读取安装状态")
	}
	var s State
	if e = decodeState(b, &s); e != nil {
		return nil, e
	}
	if s.Schema != StateSchema {
		return nil, errors.New("不支持旧版安装状态；请清理旧测试部署后重新安装")
	}
	return &s, nil
}
func (m *Manager) Managed(ctx context.Context, s *State) error {
	if s == nil || !s.Installed {
		return errors.New("服务尚未安装")
	}
	if e := m.VerifyRelease(s.Current); e != nil {
		return e
	}
	link := filepath.Join(filepath.Dir(m.P.Control), "../bin/gost")
	target, e := os.Readlink(link)
	if e != nil || target != s.Current.Binary {
		return errors.New("gost 路径不属于本工具")
	}
	target, e = os.Readlink(m.P.Control)
	if e != nil || target != s.Manager || !within(target, m.P.Managers) {
		return errors.New("管理器路径不属于本工具")
	}
	if real, err := filepath.EvalSymlinks(target); err != nil || real != target {
		return errors.New("管理器路径包含外部链接")
	}
	hash, e := Digest(target)
	if e != nil || hash != s.ManagerSHA {
		return errors.New("管理器校验失败")
	}
	for path, text := range map[string]string{m.P.Unit: UnitText, m.P.APIUnit: APIUnitText} {
		st, e := os.Lstat(path)
		if e != nil || !st.Mode().IsRegular() {
			return errors.New("发现外部 unit")
		}
		b, e := os.ReadFile(path)
		if e != nil || string(b) != text {
			return errors.New("受管 unit 被外部修改")
		}
		unit := filepath.Base(path)
		b, e = m.sys(ctx, "show", unit, "-p", "FragmentPath", "--value")
		if e != nil || strings.TrimSpace(string(b)) != path {
			return errors.New("实际 unit 路径不符")
		}
		b, e = m.sys(ctx, "show", unit, "-p", "DropInPaths", "--value")
		if e != nil || strings.TrimSpace(string(b)) != "" {
			return errors.New("发现外部 systemd drop-in")
		}
	}
	return nil
}
func (m *Manager) Conflict(ctx context.Context, s *State) error {
	if s != nil && s.Installed {
		return m.Managed(ctx, s)
	}
	for _, p := range []string{m.P.Unit, m.P.APIUnit, m.P.Unit + ".d", m.P.APIUnit + ".d", filepath.Clean(filepath.Join(filepath.Dir(m.P.Control), "../bin/gost"))} {
		if exists(p) {
			return errors.New("发现已有二进制或服务，拒绝接管")
		}
	}
	if s == nil {
		for _, p := range []string{m.P.Control, m.P.Manifest, m.P.Config, m.P.Input, m.P.Token, m.P.Managers} {
			if exists(p) {
				return errors.New("发现已有管理资源，拒绝覆盖")
			}
		}
	}
	if s != nil && exists(m.P.Control) {
		t, e := os.Readlink(m.P.Control)
		if e != nil || t != s.Manager {
			return errors.New("管理器路径不属于本工具")
		}
	}
	b, e := m.sys(ctx, "show", "gost.service", "-p", "LoadState", "--value")
	if e != nil || strings.TrimSpace(string(b)) != "not-found" {
		return errors.New("发现同名 systemd 服务")
	}
	if _, e = m.Exec(ctx, "getent", "passwd", "gost"); e == nil && (s == nil || !s.OwnsAccount) {
		return errors.New("发现已有 gost 用户")
	}
	if _, e = m.Exec(ctx, "getent", "group", "gost"); e == nil && (s == nil || !s.OwnsAccount) {
		return errors.New("发现已有 gost 组")
	}
	if p, e := m.Exec(ctx, "sh", "-c", "command -v gost"); e == nil && len(p) > 0 {
		return errors.New("PATH 中存在外部 gost")
	}
	l, e := net.Listen("tcp", "127.0.0.1:18080")
	if e != nil {
		return errors.New("管理 API 端口已占用")
	}
	l.Close()
	return nil
}
func (m *Manager) call(ctx context.Context, command string, req supervisor.Request, out any) error {
	req.Command = command
	if m.RPC != nil {
		return m.RPC(ctx, req, out)
	}
	return supervisor.RPC(ctx, m.P.Socket, req, out)
}
func (m *Manager) Status(ctx context.Context, s *State) (map[string]any, error) {
	c, e := m.ReadConfig()
	if e != nil {
		return nil, e
	}
	a := m.Active(ctx)
	v := map[string]any{"version": s.Current.Version, "arch": s.Current.Arch, "active": a, "enabled": m.Enabled(ctx), "revision": s.Revision, "activeProfile": c.ActiveProfile, "available": false}
	v["rollbackAvailable"] = s.Previous != nil
	if p := s.Previous; p != nil {
		v["rollbackTarget"] = map[string]any{"version": p.Release.Version, "activeProfile": p.Config.ActiveProfile, "active": p.Active, "enabled": p.Enabled}
	}
	if a {
		var runtime map[string]any
		if e = m.call(ctx, "status", supervisor.Request{}, &runtime); e != nil {
			return nil, e
		}
		for k, x := range runtime {
			v[k] = x
		}
	}
	return v, nil
}
func (m *Manager) Preflight(c config.Config, old *config.Config) error {
	if old != nil && c.Listener.Addr == old.Listener.Addr {
		return nil
	}
	l, e := net.Listen("tcp", c.Listener.Addr)
	if e != nil {
		return errors.New("入口 TCP 端口已占用或 IP 不属于本机")
	}
	return l.Close()
}
func (m *Manager) health(ctx context.Context, c config.Config) error {
	budget := 12 * time.Second
	for _, p := range c.Profiles {
		if p.Probe != nil {
			budget += time.Duration((float64(len(p.Nodes))/16 + 1) * p.Probe.Timeout * float64(time.Second))
		}
	}
	deadline := time.Now().Add(budget)
	host, port, _ := net.SplitHostPort(c.Listener.Addr)
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		good := m.Active(ctx)
		for _, auth := range c.Listener.Users {
			conn, e := net.DialTimeout("tcp", net.JoinHostPort(host, port), 300*time.Millisecond)
			if e != nil {
				good = false
				break
			}
			conn.SetDeadline(time.Now().Add(time.Second))
			e = socks.Authenticate(conn, auth)
			conn.Close()
			if e != nil {
				good = false
				break
			}
		}
		if good {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("服务未通过启动及 SOCKS5 认证检查")
}
func (m *Manager) apiHealth(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := m.sys(ctx, "is-active", "--quiet", "gost-deploy-api.service"); e == nil {
			c, e := net.DialTimeout("tcp", "127.0.0.1:18080", 200*time.Millisecond)
			if e == nil {
				c.Close()
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("管理 API 未通过启动检查")
}
func (m *Manager) deploy() (string, string, error) {
	hash, e := Digest(m.Self)
	if e != nil {
		return "", "", e
	}
	path := filepath.Join(m.P.Managers, hash, "gostctl")
	for _, p := range []string{filepath.Dir(m.P.Managers), m.P.Managers, filepath.Dir(path)} {
		if e = os.MkdirAll(p, 0755); e != nil {
			return "", "", e
		}
		if e = os.Chmod(p, 0755); e != nil {
			return "", "", e
		}
	}
	if exists(path) {
		d, e := Digest(path)
		if e != nil || d != hash {
			return "", "", errors.New("缓存管理器被修改")
		}
	} else {
		b, e := os.ReadFile(m.Self)
		if e != nil {
			return "", "", e
		}
		if e = Atomic(path, b, 0755, -1); e != nil {
			return "", "", e
		}
	}
	return path, hash, symlink(m.P.Control, path)
}
func (m *Manager) write(c config.Config, r Release, o native.Options, revision string) error {
	gid, e := m.Group()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(m.P.State), 0700); e != nil {
		return e
	}
	os.Chmod(filepath.Dir(m.P.State), 0700)
	if e = os.MkdirAll(filepath.Dir(m.P.Config), 0750); e != nil {
		return e
	}
	if e = os.Chmod(filepath.Dir(m.P.Config), 0750); e != nil {
		return e
	}
	if e = os.Chown(filepath.Dir(m.P.Config), os.Geteuid(), gid); e != nil {
		return e
	}
	for p, v := range map[string]any{m.P.Manifest: c, m.P.Config: native.Frontend(c, o), m.P.Input: supervisor.Input{Config: c, Options: o, Binary: filepath.Clean(filepath.Join(filepath.Dir(m.P.Control), "../bin/gost")), Revision: revision}} {
		mode, g := os.FileMode(0640), gid
		if p == m.P.Manifest {
			mode = 0600
			g = -1
		}
		if e = writeJSON(p, v, mode, g); e != nil {
			return e
		}
	}
	return nil
}

type fileSnapshot struct {
	path     string
	data     []byte
	mode     os.FileMode
	uid, gid int
	target   string
	present  bool
}

func capture(path string) (fileSnapshot, error) {
	v := fileSnapshot{path: path}
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	v.present = true
	v.mode = st.Mode().Perm()
	if st.Mode()&os.ModeSymlink != 0 {
		v.target, e = os.Readlink(path)
	} else {
		v.data, e = os.ReadFile(path)
	}
	return v, e
}

// Ownership is reconstructed from the managed path policy when restoring snapshots.
func (m *Manager) restore(files []fileSnapshot) error {
	for _, f := range files {
		if !f.present {
			if e := os.Remove(f.path); e != nil && !os.IsNotExist(e) {
				return e
			}
			continue
		}
		if f.target != "" {
			if e := symlink(f.path, f.target); e != nil {
				return e
			}
			continue
		}
		gid := -1
		if f.path == m.P.Config || f.path == m.P.Input {
			g, e := m.Group()
			if e != nil {
				return e
			}
			gid = g
		}
		if e := Atomic(f.path, f.data, f.mode, gid); e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) serviceState(ctx context.Context, a, en bool) error {
	verb := "disable"
	if en {
		verb = "enable"
	}
	if _, e := m.sys(ctx, verb, "gost.service"); e != nil {
		return e
	}
	if !a {
		_, e := m.sys(ctx, "stop", "gost.service")
		return e
	}
	return nil
}

// Apply is called under Locked. API callers must set manageAPI=false.
func (m *Manager) Apply(ctx context.Context, s *State, rel Release, c config.Config, desired *Snapshot, manageAPI bool) (err error) {
	if e := c.Validate(false); e != nil {
		return e
	}
	fresh := s == nil || !s.Installed
	managerDirectoryExisted := exists(m.P.Managers)
	options := native.Options{}
	if !fresh {
		options = s.Runtime
	} else {
		gateway, e := supervisor.FreeAddr()
		if e != nil {
			return e
		}
		options = native.Options{Gateway: gateway, Auth: config.Auth{Username: "gost-internal", Password: config.Secret()}}
	}
	rev := config.Secret()
	if e := m.ValidateNative(ctx, rel, native.Frontend(c, options)); e != nil {
		return e
	}
	for _, p := range c.Profiles {
		for _, n := range p.Nodes {
			if e := m.ValidateNative(ctx, rel, native.Worker("127.0.0.1:1081", options.Auth, &n)); e != nil {
				return e
			}
		}
	}
	var previous *Snapshot
	var old config.Config
	if !fresh {
		var e error
		old, e = m.ReadConfig()
		if e != nil {
			return e
		}
		previous = &Snapshot{s.Current, old, m.Active(ctx), m.Enabled(ctx)}
	}
	want := desired
	if want == nil {
		want = previous
	}
	if want == nil {
		want = &Snapshot{Active: true, Enabled: true}
	}
	hash, e := Digest(m.Self)
	if e != nil {
		return e
	}
	hot := !fresh && s.Current == rel && reflect.DeepEqual(c.Listener, old.Listener) && s.ManagerSHA == hash && want.Active == previous.Active && want.Enabled == previous.Enabled
	paths := []string{m.P.Config, m.P.Manifest, m.P.Input, m.P.Unit, m.P.APIUnit, m.P.State, m.P.Token, m.P.Control, filepath.Clean(filepath.Join(filepath.Dir(m.P.Control), "../bin/gost"))}
	files := []fileSnapshot{}
	for _, p := range paths {
		f, e := capture(p)
		if e != nil {
			return e
		}
		files = append(files, f)
	}
	createdAccount := false
	prepared := false
	defer func() {
		if err == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		original := err
		recoverErr := func() error {
			if hot {
				if e := m.restore(files[:3]); e != nil {
					return e
				}
				if e := m.restore(files[5:6]); e != nil {
					return e
				}
				if prepared {
					var observed supervisor.Status
					if e := m.call(recovery, "status", supervisor.Request{}, &observed); e != nil {
						return e
					}
					if observed.Revision != s.Revision {
						if e := m.call(recovery, "prepare", supervisor.Request{Config: old, Revision: s.Revision}, nil); e != nil {
							return e
						}
						return m.call(recovery, "commit", supervisor.Request{Revision: s.Revision}, nil)
					}
					return m.call(recovery, "abort", supervisor.Request{}, nil)
				}
				return nil
			}
			_, _ = m.sys(recovery, "stop", "gost.service")
			if manageAPI {
				_, _ = m.sys(recovery, "stop", "gost-deploy-api.service")
			}
			if fresh {
				_, _ = m.sys(recovery, "disable", "--now", "gost-deploy-api.service")
				_, _ = m.sys(recovery, "disable", "gost.service")
			}
			if e := m.restore(files); e != nil {
				return e
			}
			if fresh && !managerDirectoryExisted {
				if e := os.RemoveAll(m.P.Managers); e != nil {
					return e
				}
			}
			if _, e := m.sys(recovery, "daemon-reload"); e != nil {
				return e
			}
			_, _ = m.sys(recovery, "reset-failed", "gost.service")
			if previous != nil {
				if previous.Active {
					if _, e := m.sys(recovery, "start", "gost.service"); e != nil {
						return e
					}
					if e := m.health(recovery, old); e != nil {
						return e
					}
				}
				if e := m.serviceState(recovery, previous.Active, previous.Enabled); e != nil {
					return e
				}
				if manageAPI {
					if _, e := m.sys(recovery, "start", "gost-deploy-api.service"); e != nil {
						return e
					}
					return m.apiHealth(recovery)
				}
			} else if createdAccount {
				if _, e := m.Exec(recovery, "userdel", "gost"); e != nil {
					return e
				}
				_, _ = m.Exec(recovery, "groupdel", "gost")
			}
			return nil
		}()
		if recoverErr != nil {
			err = fmt.Errorf("更新失败，自动恢复未完成：%w", original)
		} else if fresh {
			err = errors.New("安装失败，已撤销新服务；下载缓存可能保留")
		} else {
			err = errors.New("操作失败，已恢复原配置、二进制及服务状态")
		}
	}()
	if hot {
		if previous.Active {
			if e = m.call(ctx, "prepare", supervisor.Request{Config: c, Revision: rev}, nil); e != nil {
				return e
			}
			prepared = true
		}
	} else {
		if fresh {
			if _, e = m.Exec(ctx, "getent", "passwd", "gost"); e != nil {
				if _, e = m.Exec(ctx, "useradd", "--system", "--user-group", "--home-dir", "/nonexistent", "--no-create-home", "--shell", "/usr/sbin/nologin", "gost"); e != nil {
					return e
				}
				createdAccount = true
			}
		} else {
			if _, e = m.sys(ctx, "stop", "gost.service"); e != nil {
				return e
			}
		}
		if _, _, e = m.deploy(); e != nil {
			return e
		}
		if e = Atomic(m.P.Unit, []byte(UnitText), 0644, -1); e != nil {
			return e
		}
		if e = Atomic(m.P.APIUnit, []byte(APIUnitText), 0644, -1); e != nil {
			return e
		}
		if !exists(m.P.Token) {
			if e = Atomic(m.P.Token, []byte(config.Secret()+"\n"), 0600, -1); e != nil {
				return e
			}
		}
	}
	if e = m.write(c, rel, options, rev); e != nil {
		return e
	}
	if !hot {
		if e = symlink(paths[8], rel.Binary); e != nil {
			return e
		}
		if _, e = m.sys(ctx, "daemon-reload"); e != nil {
			return e
		}
		_, _ = m.sys(ctx, "reset-failed", "gost.service")
		if _, e = m.sys(ctx, "start", "gost.service"); e != nil {
			return e
		}
		if e = m.health(ctx, c); e != nil {
			return e
		}
		if e = m.serviceState(ctx, want.Active, want.Enabled); e != nil {
			return e
		}
	}
	manager := sManager(s)
	if !hot {
		manager, _ = os.Readlink(m.P.Control)
	}
	next := State{StateSchema, true, true, rel, previous, rev, options, manager, hash}
	if e = writeJSON(m.P.State, next, 0600, -1); e != nil {
		return e
	}
	if prepared {
		var result supervisor.Status
		if e = m.call(ctx, "commit", supervisor.Request{Revision: rev}, &result); e != nil {
			return e
		}
		if result.Revision != rev {
			return errors.New("运行时代次确认失败")
		}
	}
	if !hot && manageAPI {
		if _, e = m.sys(ctx, "enable", "gost-deploy-api.service"); e != nil {
			return e
		}
		if _, e = m.sys(ctx, "start", "gost-deploy-api.service"); e != nil {
			return e
		}
		if e = m.apiHealth(ctx); e != nil {
			return e
		}
	}
	return nil
}
func sManager(s *State) string {
	if s == nil {
		return ""
	}
	return s.Manager
}

// Install drains the API before taking the transaction lock for a manager update.
func (m *Manager) Install(ctx context.Context, c config.Config, version, archive, sums string) error {
	return m.tlsMaintenance(ctx, func() error { return m.install(ctx, c, version, archive, sums) })
}
func (m *Manager) install(ctx context.Context, c config.Config, version, archive, sums string) error {
	prior, e := m.Load(true)
	if e != nil {
		return e
	}
	hash, e := Digest(m.Self)
	if e != nil {
		return e
	}
	stoppedAPI := false
	if prior != nil && prior.Installed && prior.ManagerSHA != hash {
		if e = m.Managed(ctx, prior); e != nil {
			return e
		}
		if _, e = m.sys(ctx, "stop", "gost-deploy-api.service"); e != nil {
			return e
		}
		stoppedAPI = true
	}
	err := m.Locked(func() error {
		s, e := m.Load(true)
		if e != nil {
			return e
		}
		if e = m.Conflict(ctx, s); e != nil {
			return e
		}
		var old *config.Config
		if s != nil && s.Installed {
			v, e := m.ReadConfig()
			if e != nil {
				return e
			}
			old = &v
			if v.Equal(c) && s.Current.Version == version && s.ManagerSHA == hash {
				return nil
			}
			if !v.Equal(c) || s.Current.Version != version {
				return errors.New("已安装；配置更新使用 configure，版本更新使用 upgrade")
			}
		}
		if e = m.Preflight(c, old); e != nil {
			return e
		}
		r, e := m.PrepareRelease(ctx, version, archive, sums)
		if e != nil {
			return e
		}
		return m.Apply(ctx, s, r, c, nil, true)
	})
	if stoppedAPI {
		_, restartErr := m.sys(context.Background(), "start", "gost-deploy-api.service")
		if err == nil {
			err = restartErr
		}
	}
	return err
}
func (m *Manager) Uninstall(ctx context.Context, s *State, purge bool) error {
	return m.tlsMaintenance(ctx, func() error {
		if e := m.uninstallAnyTLS(ctx, s, purge); e != nil {
			return e
		}
		if e := m.disableTLS(ctx, purge); e != nil {
			return e
		}
		return m.uninstall(ctx, s, purge)
	})
}
func (m *Manager) uninstall(ctx context.Context, s *State, purge bool) error {
	if s.Installed {
		if e := m.Managed(ctx, s); e != nil {
			return e
		}
		if _, e := m.sys(ctx, "disable", "--now", "gost-deploy-api.service"); e != nil {
			return e
		}
		if _, e := m.sys(ctx, "disable", "--now", "gost.service"); e != nil {
			return e
		}
		for _, p := range []string{m.P.Unit, m.P.APIUnit, filepath.Clean(filepath.Join(filepath.Dir(m.P.Control), "../bin/gost"))} {
			if e := os.Remove(p); e != nil {
				return e
			}
		}
		if _, e := m.sys(ctx, "daemon-reload"); e != nil {
			return e
		}
		_, _ = m.sys(ctx, "reset-failed", "gost.service")
		s.Installed = false
		if e := writeJSON(m.P.State, s, 0600, -1); e != nil {
			return e
		}
	}
	if purge {
		for _, p := range []string{m.P.Manifest, m.P.Config, m.P.Input, m.P.Control} {
			if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		if e := os.RemoveAll(filepath.Dir(m.P.Releases)); e != nil {
			return e
		}
		if s.OwnsAccount {
			if _, e := m.Exec(ctx, "userdel", "gost"); e != nil {
				return e
			}
			_, _ = m.Exec(ctx, "groupdel", "gost")
		}
		if e := os.RemoveAll(filepath.Dir(m.P.State)); e != nil {
			return e
		}
		for _, p := range []string{filepath.Dir(m.P.Manifest), filepath.Dir(m.P.Config)} {
			_ = os.Remove(p)
		}
	}
	return nil
}
func ReadInput(path string) (supervisor.Input, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return supervisor.Input{}, e
	}
	var v supervisor.Input
	e = json.Unmarshal(b, &v)
	return v, e
}

func (m *Manager) Runtime(ctx context.Context, req supervisor.Request, out any) error {
	return m.call(ctx, req.Command, req, out)
}
func (m *Manager) Service(ctx context.Context, verb string) error {
	if verb != "start" && verb != "stop" && verb != "restart" {
		return errors.New("不支持的服务操作")
	}
	if _, e := m.sys(ctx, verb, "gost.service"); e != nil {
		return e
	}
	if verb != "stop" {
		c, e := m.ReadConfig()
		if e != nil {
			return e
		}
		return m.health(ctx, c)
	}
	return nil
}

// Rollback is called with the same lifecycle lock as Apply, from CLI or API.
func (m *Manager) Rollback(ctx context.Context, s *State) error {
	if s.Previous == nil {
		return errors.New("没有上一份可恢复版本")
	}
	p := s.Previous
	if e := m.VerifyRelease(p.Release); e != nil {
		return e
	}
	c, e := m.ReadConfig()
	if e != nil {
		return e
	}
	if e = m.Preflight(p.Config, &c); e != nil {
		return e
	}
	return m.Apply(ctx, s, p.Release, p.Config, p, false)
}
