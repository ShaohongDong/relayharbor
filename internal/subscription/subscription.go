// Package subscription defines client exports and the isolated AnyTLS worker config.
package subscription

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/ShaohongDong/relayharbor/internal/config"
)

type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

func (u User) Validate() error {
	if !config.Identifier(u.ID) || !config.Text(u.Name, 128) || u.Port < 1024 || u.Port > 65535 ||
		!config.Text(u.Password, 255) || len(u.Token) != 43 {
		return config.ErrInvalid
	}
	for _, c := range u.Token {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-", c) {
			return config.ErrInvalid
		}
	}
	return nil
}

// JSON double-quoted strings are also YAML double-quoted scalars. Never interpolate
// user-provided labels or secrets as bare YAML.
func scalar(s string) string { b, _ := json.Marshal(s); return string(b) }

func YAML(domain string, u User, provider bool) []byte {
	name := "AnyTLS · " + u.Name
	node := fmt.Sprintf("proxies:\n  - name: %s\n    type: anytls\n    server: %s\n    port: %d\n    password: %s\n    sni: %s\n    skip-cert-verify: false\n    client-fingerprint: chrome\n    udp: false\n",
		scalar(name), scalar(domain), u.Port, scalar(u.Password), scalar(domain))
	if provider {
		return []byte(node)
	}
	return []byte("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: warning\n" + node +
		"proxy-groups:\n  - name: GOST-出口\n    type: select\n    proxies:\n      - " + scalar(name) +
		"\nrules:\n  - MATCH,GOST-出口\n")
}

func Native(u User, gateway string, auth config.Auth, cert, key string) map[string]any {
	host, port, _ := net.SplitHostPort(gateway)
	p, _ := strconv.Atoi(port)
	return map[string]any{
		"log": map[string]any{"disabled": true},
		"inbounds": []any{map[string]any{
			"type": "anytls", "tag": "client", "listen": "0.0.0.0", "listen_port": u.Port,
			"users": []any{map[string]any{"name": u.ID, "password": u.Password}},
			"tls": map[string]any{"enabled": true, "min_version": "1.2",
				"certificate_path": cert, "key_path": key},
		}},
		"outbounds": []any{map[string]any{
			"type": "socks", "tag": "scheduler", "server": host, "server_port": p,
			"version": "5", "username": auth.Username, "password": auth.Password,
			"network": "tcp",
		}},
		"route": map[string]any{
			"rules": []any{map[string]any{"network": "udp", "action": "reject"}},
			"final": "scheduler",
		},
	}
}
