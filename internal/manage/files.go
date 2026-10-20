// Package manage owns privileged lifecycle operations and durable transactions.
package manage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gitee.com/kenny3755/gost_deploy/internal/config"
	"gitee.com/kenny3755/gost_deploy/internal/supervisor"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Paths struct{ Manifest, Config, Input, State, Token, Unit, APIUnit, Control, Releases, Managers, Socket, Lock string }

func DefaultPaths() Paths {
	return Paths{"/etc/gost-deploy/config.json", "/etc/gost/gost.json", "/etc/gost/runtime.json", "/var/lib/gost-deploy/state.json", "/var/lib/gost-deploy/api-token", "/etc/systemd/system/gost.service", "/etc/systemd/system/gost-deploy-api.service", "/usr/local/sbin/gostctl", "/opt/gost-deploy/releases", "/opt/gost-deploy/managers", "/run/gost-deploy/control.sock", "/run/lock/gost-deploy.lock"}
}

type Runner func(context.Context, string, ...string) ([]byte, error)

func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return b, fmt.Errorf("系统命令失败：%s", filepath.Base(name))
	}
	return b, nil
}

type Manager struct {
	P           Paths
	Exec        Runner
	Self        string
	mu          sync.Mutex
	Group       func() (int, error)
	RPC         func(context.Context, supervisor.Request, any) error
	Journal     func(context.Context, int) ([]byte, bool, error)
	TLSProbe    func(context.Context) error
	AnyTLSProbe func(context.Context, string, int) error
}

func New() *Manager {
	self, _ := os.Executable()
	return &Manager{P: DefaultPaths(), Exec: Run, Self: self, Group: groupID}
}
func (m *Manager) Locked(fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, e := os.OpenFile(m.P.Lock, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func Atomic(path string, data []byte, mode os.FileMode, gid int) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".gost-deploy-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if gid >= 0 {
		if e = f.Chown(os.Geteuid(), gid); e != nil {
			return e
		}
	}
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func Digest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func exists(path string) bool { _, e := os.Lstat(path); return e == nil }
func within(path, root string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}
func symlink(path, target string) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	tmp := path + ".gost-deploy-new"
	_ = os.Remove(tmp)
	if e := os.Symlink(target, tmp); e != nil {
		return e
	}
	defer os.Remove(tmp)
	return os.Rename(tmp, path)
}
func groupID() (int, error) {
	b, e := os.ReadFile("/etc/group")
	if e != nil {
		return 0, e
	}
	for _, line := range strings.Split(string(b), "\n") {
		p := strings.Split(line, ":")
		if len(p) >= 3 && p[0] == "gost" {
			return strconv.Atoi(p[2])
		}
	}
	return 0, errors.New("找不到 gost 组")
}
func (m *Manager) sys(ctx context.Context, args ...string) ([]byte, error) {
	return m.Exec(ctx, "systemctl", args...)
}
func (m *Manager) Active(ctx context.Context) bool {
	_, e := m.sys(ctx, "is-active", "--quiet", "gost.service")
	return e == nil
}
func (m *Manager) Enabled(ctx context.Context) bool {
	_, e := m.sys(ctx, "is-enabled", "--quiet", "gost.service")
	return e == nil
}
func (m *Manager) ReadConfig() (config.Config, error) {
	b, e := os.ReadFile(m.P.Manifest)
	if e != nil {
		return config.Config{}, errors.New("无法读取管理配置")
	}
	return config.Parse(b, nil, false)
}
func writeJSON(path string, v any, mode os.FileMode, gid int) error {
	return Atomic(path, config.Bytes(v), mode, gid)
}
func RequireHost() error {
	if os.Geteuid() != 0 {
		return errors.New("此命令需要 root 权限")
	}
	b, e := os.ReadFile("/etc/os-release")
	if e != nil {
		return e
	}
	v := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, s, ok := strings.Cut(line, "="); ok {
			v[k] = strings.Trim(s, "\"")
		}
	}
	supported := map[string]bool{"ubuntu:20.04": true, "ubuntu:22.04": true, "ubuntu:24.04": true, "debian:12": true, "debian:13": true}
	if !supported[v["ID"]+":"+v["VERSION_ID"]] {
		return errors.New("不支持的操作系统")
	}
	if _, e = os.Stat("/run/systemd/system"); e != nil {
		return errors.New("需要运行中的 systemd")
	}
	return nil
}
func decodeState(b []byte, v any) error {
	x, e := config.DecodeJSON(b)
	if e != nil {
		return e
	}
	b, _ = json.Marshal(x)
	if e = json.Unmarshal(b, v); e != nil {
		return errors.New("安装状态无效")
	}
	return nil
}
