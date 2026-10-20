// Package config owns the strict, versioned public management configuration.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Auth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type Listener struct {
	Addr  string `json:"addr"`
	UDP   bool   `json:"udp"`
	Users []Auth `json:"users"`
}
type Node struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Addr string `json:"addr"`
	Auth *Auth  `json:"auth,omitempty"`
}
type Probe struct {
	Type           string  `json:"type"`
	Addr           string  `json:"addr,omitempty"`
	URL            string  `json:"url,omitempty"`
	Interval       float64 `json:"interval"`
	Timeout        float64 `json:"timeout"`
	ExpectedStatus int     `json:"expectedStatus,omitempty"`
}
type Profile struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Nodes []Node `json:"nodes,omitempty"`
	Probe *Probe `json:"probe,omitempty"`
}
type Config struct {
	SchemaVersion int       `json:"schemaVersion"`
	Listener      Listener  `json:"listener"`
	ActiveProfile string    `json:"activeProfile"`
	Profiles      []Profile `json:"profiles"`
}

var ErrInvalid = errors.New("配置包含缺失、无效或不支持的字段")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var hostname = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
var numericPort = regexp.MustCompile(`^[0-9]+$`)

func Identifier(s string) bool { return identifier.MatchString(s) }
func Secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Initial(addr string, udp bool) Config {
	return Config{2, Listener{addr, udp, []Auth{{"proxy", Secret()}}}, "direct", []Profile{{Name: "direct", Type: "direct"}}}
}
func Text(s string, limit int) bool {
	if !utf8.ValidString(s) || len(s) < 1 || len(s) > limit {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func Endpoint(addr string, listen bool) error {
	if !Text(addr, 1024) {
		return ErrInvalid
	}
	host, p, e := net.SplitHostPort(addr)
	if e != nil {
		return ErrInvalid
	}
	port, e := strconv.Atoi(p)
	low := 1
	if listen {
		low = 1024
	}
	if e != nil || !numericPort.MatchString(p) || port < low || port > 65535 {
		return ErrInvalid
	}
	if ip := net.ParseIP(host); ip == nil && (listen || !hostname.MatchString(host) || strings.HasPrefix(addr, "[")) {
		return ErrInvalid
	}
	return nil
}
func (c Config) Active() Profile {
	for _, p := range c.Profiles {
		if p.Name == c.ActiveProfile {
			return p
		}
	}
	return Profile{}
}
func (c Config) Clone() Config {
	b, _ := json.Marshal(c)
	var d Config
	_ = json.Unmarshal(b, &d)
	return d
}
func (c Config) Equal(d Config) bool { return reflect.DeepEqual(c, d) }
func validateAuth(a Auth, placeholders bool) error {
	if !Text(a.Username, 255) || !Text(a.Password, 255) || (!placeholders && (strings.HasPrefix(a.Username, "CHANGE_ME") || strings.HasPrefix(a.Password, "CHANGE_ME"))) {
		return ErrInvalid
	}
	return nil
}
func (c Config) Validate(placeholders bool) error {
	if c.SchemaVersion != 2 || Endpoint(c.Listener.Addr, true) != nil || len(c.Listener.Users) == 0 || len(c.Profiles) == 0 {
		return ErrInvalid
	}
	names := map[string]bool{}
	for _, u := range c.Listener.Users {
		if validateAuth(u, placeholders) != nil || names[u.Username] {
			return ErrInvalid
		}
		names[u.Username] = true
	}
	names = map[string]bool{}
	for _, p := range c.Profiles {
		if !Identifier(p.Name) || names[p.Name] {
			return ErrInvalid
		}
		names[p.Name] = true
		if p.Type == "direct" {
			if len(p.Nodes) > 0 || p.Probe != nil {
				return ErrInvalid
			}
			continue
		}
		if p.Type != "pool" || len(p.Nodes) == 0 || p.Probe == nil {
			return ErrInvalid
		}
		ids := map[string]bool{}
		for _, n := range p.Nodes {
			if !Identifier(n.ID) || ids[n.ID] || (n.Type != "http" && n.Type != "socks5") || Endpoint(n.Addr, false) != nil {
				return ErrInvalid
			}
			ids[n.ID] = true
			if n.Auth != nil && validateAuth(*n.Auth, placeholders) != nil {
				return ErrInvalid
			}
		}
		v := p.Probe
		if !(v.Interval >= 1 && v.Interval <= 86400 && v.Timeout >= 0.1 && v.Timeout <= 60) {
			return ErrInvalid
		}
		switch v.Type {
		case "tcp":
			if Endpoint(v.Addr, false) != nil || v.URL != "" || v.ExpectedStatus != 0 {
				return ErrInvalid
			}
		case "http":
			u, e := url.Parse(v.URL)
			if e != nil || !Text(v.URL, 4096) || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || v.Addr != "" || v.ExpectedStatus < 100 || v.ExpectedStatus > 599 {
				return ErrInvalid
			}
			for _, r := range v.URL {
				if r > 127 || r == ' ' {
					return ErrInvalid
				}
			}
			port := u.Port()
			if port == "" {
				port = "80"
				if u.Scheme == "https" {
					port = "443"
				}
			}
			if Endpoint(net.JoinHostPort(u.Hostname(), port), false) != nil {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	if !names[c.ActiveProfile] {
		return ErrInvalid
	}
	return nil
}

// DecodeJSON rejects duplicate keys, invalid Unicode, trailing values and NaN.
func DecodeJSON(data []byte) (any, error) {
	if !utf8.Valid(data) || !validSurrogates(data) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var read func() (any, error)
	read = func() (any, error) {
		t, e := d.Token()
		if e != nil {
			return nil, ErrInvalid
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				m := map[string]any{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, ErrInvalid
					}
					s, ok := k.(string)
					if !ok {
						return nil, ErrInvalid
					}
					if _, yes := m[s]; yes {
						return nil, ErrInvalid
					}
					v, e := read()
					if e != nil {
						return nil, e
					}
					m[s] = v
				}
				if _, e = d.Token(); e != nil {
					return nil, ErrInvalid
				}
				return m, nil
			case '[':
				a := []any{}
				for d.More() {
					v, e := read()
					if e != nil {
						return nil, e
					}
					a = append(a, v)
				}
				if _, e = d.Token(); e != nil {
					return nil, ErrInvalid
				}
				return a, nil
			}
			return nil, ErrInvalid
		}
		return t, nil
	}
	v, e := read()
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	return v, nil
}

// encoding/json replaces unpaired surrogate escapes; reject them before decoding.
func validSurrogates(b []byte) bool {
	quoted := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func object(v any, allowed, required string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	for k := range m {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return nil, ErrInvalid
		}
	}
	for _, k := range strings.Fields(required) {
		if _, ok := m[k]; !ok {
			return nil, ErrInvalid
		}
	}
	return m, nil
}
func authObject(v any, old *Auth, merge bool) (map[string]any, error) {
	allowed := "username password"
	required := allowed
	if merge {
		allowed += " passwordSet"
		required = "username"
	}
	m, e := object(v, allowed, required)
	if e != nil {
		return nil, e
	}
	delete(m, "passwordSet")
	if merge {
		if _, ok := m["password"]; !ok && old != nil && m["username"] == old.Username {
			m["password"] = old.Password
		}
	}
	return m, nil
}
func Parse(data []byte, current *Config, placeholders bool) (Config, error) {
	bad := Config{}
	v, e := DecodeJSON(data)
	if e != nil {
		return bad, e
	}
	m, e := object(v, "schemaVersion listener activeProfile profiles", "schemaVersion listener activeProfile profiles")
	if e != nil {
		return bad, e
	}
	l, e := object(m["listener"], "addr udp users", "addr udp users")
	if e != nil {
		return bad, e
	}
	if _, ok := l["udp"].(bool); !ok {
		return bad, ErrInvalid
	}
	users, ok := l["users"].([]any)
	if !ok {
		return bad, ErrInvalid
	}
	for i, u := range users {
		var old *Auth
		if um, ok := u.(map[string]any); ok && current != nil {
			for _, a := range current.Listener.Users {
				if a.Username == um["username"] {
					x := a
					old = &x
				}
			}
		}
		users[i], e = authObject(u, old, current != nil)
		if e != nil {
			return bad, e
		}
	}
	profiles, ok := m["profiles"].([]any)
	if !ok {
		return bad, ErrInvalid
	}
	for _, pv := range profiles {
		p, e := object(pv, "name type nodes probe", "name type")
		if e != nil {
			return bad, e
		}
		if p["type"] == "direct" {
			if _, e = object(p, "name type", "name type"); e != nil {
				return bad, e
			}
			continue
		}
		if _, e = object(p, "name type nodes probe", "name type nodes probe"); e != nil {
			return bad, e
		}
		nodes, ok := p["nodes"].([]any)
		if !ok {
			return bad, ErrInvalid
		}
		for _, nv := range nodes {
			n, e := object(nv, "id type addr auth", "id type addr")
			if e != nil {
				return bad, e
			}
			if a, yes := n["auth"]; yes && a != nil {
				var old *Auth
				if current != nil {
					for _, op := range current.Profiles {
						if op.Name == p["name"] {
							for _, on := range op.Nodes {
								if on.ID == n["id"] {
									old = on.Auth
								}
							}
						}
					}
				}
				n["auth"], e = authObject(a, old, current != nil)
				if e != nil {
					return bad, e
				}
			}
		}
		pr, e := object(p["probe"], "type addr url interval timeout expectedStatus", "type")
		if e != nil {
			return bad, e
		}
		if pr["type"] == "tcp" {
			_, e = object(pr, "type addr interval timeout", "type addr")
		} else if pr["type"] == "http" {
			_, e = object(pr, "type url interval timeout expectedStatus", "type url")
			if _, ok := pr["expectedStatus"]; !ok {
				pr["expectedStatus"] = 200
			}
		} else {
			return bad, ErrInvalid
		}
		if e != nil {
			return bad, e
		}
		if _, ok := pr["interval"]; !ok {
			pr["interval"] = 30
		}
		if _, ok := pr["timeout"]; !ok {
			pr["timeout"] = 5
		}
	}
	b, e := json.Marshal(m)
	if e != nil {
		return bad, ErrInvalid
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return bad, ErrInvalid
	}
	if e = c.Validate(placeholders); e != nil {
		return bad, e
	}
	return c, nil
}
func (c Config) Redacted() any {
	b, _ := json.Marshal(c)
	v, _ := DecodeJSON(b)
	m := v.(map[string]any)
	for _, u := range m["listener"].(map[string]any)["users"].([]any) {
		a := u.(map[string]any)
		delete(a, "password")
		a["passwordSet"] = true
	}
	for _, pv := range m["profiles"].([]any) {
		p := pv.(map[string]any)
		if nodes, ok := p["nodes"].([]any); ok {
			for _, nv := range nodes {
				n := nv.(map[string]any)
				if a, ok := n["auth"].(map[string]any); ok {
					delete(a, "password")
					a["passwordSet"] = true
				}
			}
		}
	}
	return m
}
func Bytes(v any) []byte {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		panic(fmt.Sprintf("internal JSON error: %T", v))
	}
	return append(b, '\n')
}
