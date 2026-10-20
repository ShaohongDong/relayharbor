package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixture() Config {
	c := Initial("127.0.0.1:1080", true)
	c.Listener.Users[0].Password = "special :$()'\"\\ 😀"
	c.Profiles = append(c.Profiles, Profile{Name: "mixed", Type: "pool", Nodes: []Node{{ID: "a", Type: "http", Addr: "proxy.example:80", Auth: &Auth{Username: "up", Password: "secret"}}, {ID: "b", Type: "socks5", Addr: "[::1]:1080"}}, Probe: &Probe{Type: "tcp", Addr: "example.com:443", Interval: 30, Timeout: 5}})
	return c
}
func TestPolicy(t *testing.T) {
	base := fixture()
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"schema", func(c *Config) { c.SchemaVersion = 1 }}, {"empty-users", func(c *Config) { c.Listener.Users = nil }}, {"empty-password", func(c *Config) { c.Listener.Users[0].Password = "" }}, {"wire-limit", func(c *Config) { c.Listener.Users[0].Password = strings.Repeat("é", 128) }}, {"control", func(c *Config) { c.Listener.Users[0].Password = "a\nb" }}, {"placeholder", func(c *Config) { c.Listener.Users[0].Password = "CHANGE_ME_password" }}, {"duplicate-user", func(c *Config) { c.Listener.Users = append(c.Listener.Users, c.Listener.Users[0]) }}, {"listener-domain", func(c *Config) { c.Listener.Addr = "example.com:1080" }}, {"privileged-port", func(c *Config) { c.Listener.Addr = "127.0.0.1:80" }}, {"unbracketed-v6", func(c *Config) { c.Listener.Addr = "::1:1080" }}, {"empty-profiles", func(c *Config) { c.Profiles = nil }}, {"missing-active", func(c *Config) { c.ActiveProfile = "missing" }}, {"duplicate-profile", func(c *Config) { c.Profiles = append(c.Profiles, c.Profiles[0]) }}, {"duplicate-node", func(c *Config) { c.Profiles[1].Nodes = append(c.Profiles[1].Nodes, c.Profiles[1].Nodes[0]) }}, {"bad-type", func(c *Config) { c.Profiles[1].Nodes[0].Type = "https" }}, {"empty-pool", func(c *Config) { c.Profiles[1].Nodes = nil }}, {"missing-probe", func(c *Config) { c.Profiles[1].Probe = nil }}, {"timeout", func(c *Config) { c.Profiles[1].Probe.Timeout = 0 }}, {"interval", func(c *Config) { c.Profiles[1].Probe.Interval = 86401 }}, {"direct-nodes", func(c *Config) { c.Profiles[0].Nodes = c.Profiles[1].Nodes }}, {"upstream-port", func(c *Config) { c.Profiles[1].Nodes[0].Addr = "x:65536" }},
	}
	if e := base.Validate(false); e != nil {
		t.Fatal(e)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base.Clone()
			tt.change(&c)
			if c.Validate(false) == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}
func TestStrictJSON(t *testing.T) {
	base := string(Bytes(fixture()))
	for _, body := range []string{`{"x":1,"x":2}`, `{} {}`, `{"x":NaN}`, `{"x":"\ud800"}`, strings.Replace(base, `"udp": true`, `"udp": null`, 1), strings.Replace(base, `"udp": true`, `"UDP": true`, 1), strings.Replace(base, `"schemaVersion": 2`, `"schemaVersion": 2, "extra": 1`, 1), `{"services":[]}`, strings.Replace(base, `"type": "direct"`, `"type": "direct", "nodes": null`, 1)} {
		if _, e := Parse([]byte(body), nil, false); e == nil {
			t.Fatalf("accepted invalid JSON: %.60s", body)
		}
	}
}
func TestDefaultsAndHTTPProbe(t *testing.T) {
	c := fixture()
	var v map[string]any
	json.Unmarshal(Bytes(c), &v)
	profiles := v["profiles"].([]any)
	p := profiles[1].(map[string]any)
	p["probe"] = map[string]any{"type": "http", "url": "https://example.com/health"}
	good, e := Parse(Bytes(v), nil, false)
	if e != nil {
		t.Fatal(e)
	}
	if *good.Profiles[1].Probe != (Probe{Type: "http", URL: "https://example.com/health", Interval: 30, Timeout: 5, ExpectedStatus: 200}) {
		t.Fatal("defaults")
	}
	for _, url := range []string{"http://user:pass@example.com/", "file:///etc/passwd", "https://example.com/#x", "http://example.com:0/", "http://example.com/空格"} {
		p["probe"].(map[string]any)["url"] = url
		if _, e := Parse(Bytes(v), nil, false); e == nil {
			t.Fatal("invalid probe accepted")
		}
	}
}
func TestSecrets(t *testing.T) {
	c := fixture()
	redacted := c.Redacted()
	b := Bytes(redacted)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), c.Listener.Users[0].Password) {
		t.Fatal("secret leaked")
	}
	next, e := Parse(b, &c, false)
	if e != nil || !next.Equal(c) {
		t.Fatalf("round trip: %v", e)
	}
	v := redacted.(map[string]any)
	n := v["profiles"].([]any)[1].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	n["id"] = "new-id"
	if _, e = Parse(Bytes(v), &c, false); e == nil {
		t.Fatal("new identity inherited password")
	}
	n["auth"] = nil
	if _, e = Parse(Bytes(v), &c, false); e != nil {
		t.Fatal(e)
	}
}

func TestUnicodeAndPortSyntax(t *testing.T) {
	for _, s := range []string{`{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0041"}`} {
		if _, e := DecodeJSON([]byte(s)); e == nil {
			t.Fatal("unpaired surrogate accepted")
		}
	}
	for _, s := range []string{`{"x":"\ud83d\ude00"}`, `{"x":"�"}`, `{"x":"\\ud800"}`} {
		if _, e := DecodeJSON([]byte(s)); e != nil {
			t.Fatal("valid Unicode rejected", e)
		}
	}
	if Endpoint("127.0.0.1:+1080", true) == nil {
		t.Fatal("signed port accepted")
	}
}
