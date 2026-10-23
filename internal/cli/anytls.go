package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
)

func anyTLSCommand(ctx context.Context, m *manage.Manager, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：anytls setup|status|enable|disable|user")
	}
	op := args[0]
	args = args[1:]
	if op == "user" {
		if len(args) == 0 {
			return errors.New("用法：anytls user add|rename|route|enable|disable|delete|reset-token|reset-password|credentials --id ID")
		}
		op = args[0]
		args = args[1:]
		if op == "enable" || op == "disable" {
			op = "user-" + op
		}
	}
	var req manage.AnyTLSRequest
	var archive string
	f, e := parse("anytls "+op, args, func(f *flag.FlagSet) {
		switch op {
		case "setup":
			f.StringVar(&archive, "sing-box-archive", "", "")
		case "add":
			f.StringVar(&req.ID, "id", "", "")
			f.StringVar(&req.Name, "name", "", "")
			f.IntVar(&req.Port, "port", 0, "")
			f.StringVar(&req.Profile, "profile", "", "")
		case "rename":
			f.StringVar(&req.ID, "id", "", "")
			f.StringVar(&req.Name, "name", "", "")
		case "route":
			f.StringVar(&req.ID, "id", "", "")
			f.StringVar(&req.Profile, "profile", "", "")
		case "user-enable", "user-disable", "delete", "reset-token", "reset-password", "credentials":
			f.StringVar(&req.ID, "id", "", "")
		}
	})
	if e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("不支持的位置参数")
	}
	if e = manage.RequireHost(); e != nil {
		return e
	}
	if op == "setup" {
		if e = m.AnyTLSSetup(ctx, archive); e != nil {
			return e
		}
		op = "status"
	}
	data, rev, e := m.AnyTLS(ctx, "status", manage.AnyTLSRequest{}, "")
	if e != nil {
		return e
	}
	if op != "status" {
		data, rev, e = m.AnyTLS(ctx, op, req, rev)
		if e != nil {
			return e
		}
	}
	_, e = out.Write(config.Bytes(map[string]any{"data": data, "revision": rev}))
	return e
}
