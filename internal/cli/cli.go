// Package cli implements the public command line and the two service entrypoints.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/ShaohongDong/relayharbor/internal/api"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
	"github.com/ShaohongDong/relayharbor/internal/supervisor"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

var Version = "dev"
var Commit = "unknown"

func parse(name string, args []string, setup func(*flag.FlagSet)) (*flag.FlagSet, error) {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	setup(f)
	if e := f.Parse(args); e != nil {
		return nil, fmt.Errorf("参数无效：%s", name)
	}
	return f, nil
}
func read(path string) (config.Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return config.Config{}, errors.New("无法读取候选配置")
	}
	return config.Parse(b, nil, false)
}
func Main(ctx context.Context, args []string, out io.Writer) error {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			fmt.Fprintln(out, "用法：gostctl COMMAND [OPTIONS]\n命令：init check install configure profile upgrade rollback start stop restart status logs uninstall version tls anytls auth\nauth: reset\nanytls: setup [--sing-box-archive FILE] | status | enable | disable\nanytls user: add --id ID --name NAME [--port PORT] [--profile NAME] | rename --id ID --name NAME | route --id ID --profile NAME（空字符串跟随当前 profile） | enable/disable/delete/reset-token/reset-password/credentials --id ID\ninit: --output FILE [--listen IP:PORT] [--udp]\ncheck/configure: --config FILE; install: [--config FILE] [--setup-host HOST] [--setup-port PORT]\ninstall/upgrade: --version VERSION [--archive FILE --checksums FILE]\nprofile: list | use NAME | probe NAME\ntls: setup --domain DOMAIN --email EMAIL --accept-tos [--lego-archive FILE --lego-checksums FILE] | status | renew | reload | disable\ninstall: [--domain DOMAIN --email EMAIL --accept-tos]\nuninstall: [--purge]    logs: [--follow]")
			return nil
		}
	}
	if len(args) == 0 {
		return errors.New("用法：gostctl init|check|install|configure|profile|upgrade|rollback|start|stop|restart|status|logs|uninstall|version")
	}
	name := args[0]
	args = args[1:]
	m := manage.New()
	if name == "_setup" {
		if e := manage.RequireHost(); e != nil {
			return e
		}
		if len(args) != 1 {
			return errors.New("需要部署任务 ID")
		}
		return m.RunSetup(ctx, args[0])
	}
	if name == "auth" {
		if e := manage.RequireHost(); e != nil {
			return e
		}
		if len(args) != 1 || args[0] != "reset" {
			return errors.New("用法：gostctl auth reset")
		}
		code, e := m.ResetAuth(ctx)
		if e != nil {
			return e
		}
		fmt.Fprintf(out, "初始化地址：%s\n一次性初始化码：%s\n", m.SetupAddress(), code)
		return nil
	}
	if name == "_anytls" {
		if len(args) != 1 {
			return errors.New("内部 AnyTLS 命令需要用户 ID")
		}
		return m.AnyTLSExec(args[0])
	}
	if name == "anytls" {
		return anyTLSCommand(ctx, m, args, out)
	}
	if name == "_acme" {
		if e := manage.RequireHost(); e != nil {
			return e
		}
		return m.TLSExec(args)
	}
	if name == "tls" || name == "_tls" {
		return tlsCommand(ctx, m, name, args, out)
	}
	if name == "version" {
		if len(args) != 0 {
			return errors.New("version 不接受参数")
		}
		fmt.Fprintf(out, "gostctl %s commit=%s go=%s %s/%s\n", Version, Commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	}
	if name == "_runtime" {
		if len(args) != 0 {
			return errors.New("内部命令不接受参数")
		}
		v, e := manage.ReadInput(m.P.Input)
		if e != nil {
			return e
		}
		return supervisor.New(v, "/run/gost-deploy").Run(ctx)
	}
	if name == "_api" {
		if e := manage.RequireHost(); e != nil {
			return e
		}
		return api.Serve(ctx, m)
	}
	if name == "init" {
		var path, addr string
		var udp bool
		f, e := parse(name, args, func(f *flag.FlagSet) {
			f.StringVar(&path, "output", "", "")
			f.StringVar(&addr, "listen", config.DefaultListen(), "")
			f.BoolVar(&udp, "udp", false, "")
		})
		if e != nil {
			return e
		}
		if path == "" || f.NArg() != 0 {
			return errors.New("init 需要 --output")
		}
		c := config.Initial(addr, udp)
		if e = c.Validate(false); e != nil {
			return e
		}
		file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return errors.New("无法创建配置；不会覆盖已有文件")
		}
		_, e = file.Write(config.Bytes(c))
		ce := file.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		fmt.Fprintln(out, "配置已生成（权限 0600），账号 proxy；请在文件内查看随机密码。")
		return nil
	}
	var path, version, archive, sums string
	var follow, purge bool
	var setupHost string
	var setupPort int
	var tlsOptions manage.TLSOptions
	f, e := parse(name, args, func(f *flag.FlagSet) {
		switch name {
		case "check", "configure":
			f.StringVar(&path, "config", "", "")
		case "install", "upgrade":
			f.StringVar(&version, "version", manage.DefaultVersion, "")
			f.StringVar(&archive, "archive", "", "")
			f.StringVar(&sums, "checksums", "", "")
			if name == "install" {
				f.StringVar(&path, "config", "", "")
				tlsFlags(f, &tlsOptions)
				f.StringVar(&setupHost, "setup-host", "", "")
				f.IntVar(&setupPort, "setup-port", 0, "")
			}
		case "logs":
			f.BoolVar(&follow, "follow", false, "")
		case "uninstall":
			f.BoolVar(&purge, "purge", false, "")
		}
	})
	if e != nil {
		return e
	}
	if name != "profile" && f.NArg() != 0 {
		return errors.New("不支持的位置参数")
	}
	if name == "check" {
		if path == "" {
			return errors.New("需要 --config")
		}
		if _, e = read(path); e != nil {
			return e
		}
		fmt.Fprintln(out, "配置静态检查通过；尚未验证运行、网络和 UDP。")
		return nil
	}
	if e = manage.RequireHost(); e != nil {
		return e
	}
	if name == "configure" || name == "profile" {
		var data json.RawMessage
		var rev string
		if name == "configure" {
			if path == "" {
				return errors.New("需要 --config")
			}
			body, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			_, rev, e = api.Client(ctx, m.APISocket(), "GET", "/config", nil, "")
			if e != nil {
				return e
			}
			data, rev, e = api.Client(ctx, m.APISocket(), "PUT", "/config", json.RawMessage(body), rev)
			if e != nil {
				return e
			}
		} else {
			a := f.Args()
			if len(a) == 1 && a[0] == "list" {
				data, rev, e = api.Client(ctx, m.APISocket(), "GET", "/profiles", nil, "")
			} else if len(a) == 2 && a[0] == "probe" {
				data, rev, e = api.Client(ctx, m.APISocket(), "POST", api.ProfilePath(a[1]), map[string]any{}, "")
			} else if len(a) == 2 && a[0] == "use" {
				_, rev, e = api.Client(ctx, m.APISocket(), "GET", "/config", nil, "")
				if e == nil {
					data, rev, e = api.Client(ctx, m.APISocket(), "PUT", "/active-profile", map[string]string{"name": a[1]}, rev)
				}
			} else {
				return errors.New("用法：profile list|use NAME|probe NAME")
			}
			if e != nil {
				return e
			}
		}
		_, e = out.Write(config.Bytes(map[string]any{"data": data, "revision": rev}))
		return e
	}
	if name == "install" {
		if path == "" {
			path = m.P.Manifest
			if _, e := os.Stat(path); os.IsNotExist(e) {
				f, e := os.CreateTemp("", "relayharbor-config-*")
				if e != nil {
					return e
				}
				path = f.Name()
				defer os.Remove(path)
				if e = f.Chmod(0600); e != nil {
					f.Close()
					return e
				}
				_, e = f.Write(config.Bytes(config.Initial(config.DefaultListen(), false)))
				ce := f.Close()
				if e != nil {
					return e
				}
				if ce != nil {
					return ce
				}
			}
		}
		withTLS := tlsOptions.Domain != "" || tlsOptions.Email != "" || tlsOptions.AcceptTOS || tlsOptions.Archive != "" || tlsOptions.Checksums != ""
		if withTLS {
			if e = manage.ValidateTLSOptions(tlsOptions); e != nil {
				return e
			}
		}
		c, e := read(path)
		if e != nil {
			return e
		}
		version, e = manage.Version(version)
		if e != nil {
			return e
		}
		if e = m.Install(ctx, c, version, archive, sums); e != nil {
			return e
		}
		if withTLS {
			if e = m.TLSSetup(ctx, tlsOptions); e != nil {
				return fmt.Errorf("GOST 已安装；HTTPS 部署失败，可通过本机或 SSH 隧道恢复：%w", e)
			}
		}
		code, fingerprint, e := m.ConfigureAccess(ctx, setupHost, setupPort)
		if e != nil {
			return fmt.Errorf("核心服务已安装，管理入口配置失败，可重新执行安装：%w", e)
		}
		fmt.Fprintf(out, "操作完成。\n管理页面：%s\n自签名证书 SHA-256：%s\n", m.SetupAddress(), fingerprint)
		if code != "" {
			fmt.Fprintf(out, "一次性初始化码：%s\n请在浏览器核对证书指纹后设置管理员密码。\n", code)
		}
		return nil
	}
	// Stop accepting API work before taking the global lock during uninstall.
	apiWasActive := false
	if name == "uninstall" {
		s, e := m.Load(false)
		if e != nil {
			return e
		}
		if s.Installed {
			if e = m.Managed(ctx, s); e != nil {
				return e
			}
			_, ae := m.Exec(ctx, "systemctl", "is-active", "--quiet", "gost-deploy-api.service")
			apiWasActive = ae == nil
			if _, e = m.Exec(ctx, "systemctl", "stop", "gost-deploy-api.service"); e != nil {
				return e
			}
		}
	}
	err := m.Locked(func() error {
		s, e := m.Load(false)
		if e != nil {
			return e
		}
		if name == "uninstall" {
			if e = m.Uninstall(ctx, s, purge); e != nil {
				return e
			}
			fmt.Fprintln(out, "卸载完成。")
			return nil
		}
		if e = m.Managed(ctx, s); e != nil {
			return e
		}
		switch name {
		case "status":
			status, e := m.Status(ctx, s)
			if e != nil {
				return e
			}
			if _, e = out.Write(config.Bytes(status)); e != nil {
				return e
			}
			if status["active"] != true {
				return errors.New("服务当前未运行")
			}
			return nil
		case "start", "stop", "restart":
			if e = m.Service(ctx, name); e != nil {
				return e
			}
		case "logs":
			argv := []string{"journalctl", "-u", "gost.service", "--no-pager"}
			if follow {
				argv = append(argv, "-f")
			} else {
				argv = append(argv, "-n", "100")
			}
			binary, e := exec.LookPath("journalctl")
			if e != nil {
				return e
			}
			return syscall.Exec(binary, argv, os.Environ())
		case "upgrade":
			explicit := false
			f.Visit(func(v *flag.Flag) {
				if v.Name == "version" {
					explicit = true
				}
			})
			if !explicit {
				return errors.New("upgrade 需要 --version")
			}
			version, e = manage.Version(version)
			if e != nil {
				return e
			}
			if version == s.Current.Version {
				break
			}
			c, e := m.ReadConfig()
			if e != nil {
				return e
			}
			r, e := m.PrepareRelease(ctx, version, archive, sums)
			if e != nil {
				return e
			}
			if e = m.Apply(ctx, s, r, c, nil, false); e != nil {
				return e
			}
		case "rollback":
			if e = m.Rollback(ctx, s); e != nil {
				return e
			}
		default:
			return errors.New("未知命令")
		}
		fmt.Fprintln(out, "操作完成。")
		return nil
	})
	if err != nil && apiWasActive {
		recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, restartErr := m.Exec(recovery, "systemctl", "start", "gost-deploy-api.service")
		err = errors.Join(err, restartErr)
	}
	return err
}
