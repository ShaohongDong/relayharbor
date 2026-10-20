package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"gitee.com/kenny3755/gost_deploy/internal/config"
	"gitee.com/kenny3755/gost_deploy/internal/manage"
	"io"
)

func tlsFlags(f *flag.FlagSet, o *manage.TLSOptions) {
	f.StringVar(&o.Domain, "domain", "", "")
	f.StringVar(&o.Email, "email", "", "")
	f.BoolVar(&o.AcceptTOS, "accept-tos", false, "")
	f.StringVar(&o.Archive, "lego-archive", "", "")
	f.StringVar(&o.Checksums, "lego-checksums", "", "")
}

func tlsCommand(ctx context.Context, m *manage.Manager, command string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：tls setup|status|renew|reload|disable")
	}
	op := args[0]
	var o manage.TLSOptions
	f, e := parse("tls "+op, args[1:], func(f *flag.FlagSet) {
		if command == "tls" && op == "setup" {
			tlsFlags(f, &o)
		}
	})
	if e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("不支持的位置参数")
	}
	if op != "setup" && op != "status" && op != "renew" && op != "reload" && op != "disable" {
		return errors.New("未知 HTTPS 操作")
	}
	if command == "_tls" && op != "renew" && op != "reload" {
		return errors.New("不支持的内部 HTTPS 操作")
	}
	if op == "setup" {
		if e = manage.ValidateTLSOptions(o); e != nil {
			return e
		}
	}
	if e = manage.RequireHost(); e != nil {
		return e
	}
	if command == "_tls" {
		return m.TLSRun(ctx, op)
	}
	// TLS work does not hold the proxy configuration lock during network calls.
	if e = m.Locked(func() error {
		s, e := m.Load(false)
		if e != nil {
			return e
		}
		return m.Managed(ctx, s)
	}); e != nil {
		return e
	}
	switch op {
	case "setup":
		e = m.TLSSetup(ctx, o)
	case "disable":
		e = m.TLSDisable(ctx, false)
	case "renew", "reload":
		e = m.TLSStart(ctx, op)
		if e == nil {
			fmt.Fprintln(out, "HTTPS 任务已提交；使用 gostctl tls status 查看结果。")
			return nil
		}
	case "status":
		var s manage.TLSStatus
		s, e = m.TLSStatus(ctx)
		if e == nil {
			_, e = out.Write(config.Bytes(s))
		}
		return e
	}
	if e == nil {
		fmt.Fprintln(out, "HTTPS 操作完成。")
	}
	return e
}
