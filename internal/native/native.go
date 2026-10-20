// Package native generates allow-listed native GOST configurations.
package native

import "github.com/ShaohongDong/relayharbor/internal/config"

type M = map[string]any

func Service(addr string, users []config.Auth, udp bool) M {
	return M{"services": []any{M{"name": "socks5", "addr": addr, "handler": M{"type": "socks5", "auther": "users", "metadata": M{"udp": udp, "notls": true}}, "listener": M{"type": "tcp"}}}, "authers": []any{M{"name": "users", "auths": users}}}
}
func Chain(c M, n config.Node) M {
	co := M{"type": n.Type}
	if n.Auth != nil {
		co["auth"] = n.Auth
	}
	if n.Type == "socks5" {
		co["metadata"] = M{"notls": true}
	}
	c["services"].([]any)[0].(M)["handler"].(M)["chain"] = "upstream"
	c["chains"] = []any{M{"name": "upstream", "hops": []any{M{"name": "single", "nodes": []any{M{"name": "upstream", "addr": n.Addr, "connector": co, "dialer": M{"type": "tcp"}}}}}}}
	return c
}

type Options struct {
	Gateway string      `json:"gateway"`
	Auth    config.Auth `json:"auth"`
}

func Frontend(c config.Config, o Options) M {
	return Chain(Service(c.Listener.Addr, c.Listener.Users, c.Listener.UDP), config.Node{Type: "socks5", Addr: o.Gateway, Auth: &o.Auth})
}
func Worker(addr string, a config.Auth, n *config.Node) M {
	c := Service(addr, []config.Auth{a}, n == nil)
	if n != nil {
		return Chain(c, *n)
	}
	return c
}
