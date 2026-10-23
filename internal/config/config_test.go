package config

import (
	"encoding/json"
	"os"
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
func TestIPv6Endpoints(t *testing.T) {
	for _, addr := range []string{"[::]:1080", "[::1]:1080", "[2001:db8::1]:1080"} {
		if Endpoint(addr, true) != nil || Endpoint(addr, false) != nil {
			t.Fatal("IPv6 endpoint rejected", addr)
		}
	}
	for _, addr := range []string{"::1:1080", "[fe80::1%eth0]:1080", "[proxy.example]:1080", "[::]:80"} {
		if Endpoint(addr, true) == nil {
			t.Fatal("invalid listener accepted", addr)
		}
	}
	if Endpoint("[example.com]:443", false) == nil {
		t.Fatal("bracketed hostname accepted")
	}
	want := "0.0.0.0:1080"
	if DualStack() {
		want = "[::]:1080"
	}
	if DefaultListen() != want || Endpoint(DefaultListen(), true) != nil {
		t.Fatal("default listener", DefaultListen())
	}
}
func entryFixture() Config {
	c := fixture()
	c.Entries = []Entry{{Name: "office", Addr: "127.0.0.1:1081", Users: []Auth{{Username: "alice", Password: "alice-secret"}}, Profile: "mixed"}}
	return c
}
func TestEntries(t *testing.T) {
	base := entryFixture()
	if e := base.Validate(false); e != nil {
		t.Fatal(e)
	}
	direct := base.Clone()
	direct.Entries[0].Profile, direct.Entries[0].UDP = "direct", true
	if e := direct.Validate(false); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*Config){
		"bad-name":       func(c *Config) { c.Entries[0].Name = "a:b" },
		"duplicate-name": func(c *Config) { e := c.Entries[0]; e.Addr = "127.0.0.1:1082"; c.Entries = append(c.Entries, e) },
		"listener-port":  func(c *Config) { c.Entries[0].Addr = "[::1]:1080" },
		"entry-port": func(c *Config) {
			e := c.Entries[0]
			e.Name = "other"
			e.Addr = "[::1]:1081"
			c.Entries = append(c.Entries, e)
		},
		"no-users":        func(c *Config) { c.Entries[0].Users = nil },
		"duplicate-user":  func(c *Config) { c.Entries[0].Users = append(c.Entries[0].Users, c.Entries[0].Users[0]) },
		"missing-profile": func(c *Config) { c.Entries[0].Profile = "missing" },
		"pool-udp":        func(c *Config) { c.Entries[0].UDP = true },
		"privileged":      func(c *Config) { c.Entries[0].Addr = "127.0.0.1:443" },
		"removed-profile": func(c *Config) { c.Profiles = c.Profiles[:1] },
	} {
		c := base.Clone()
		change(&c)
		if c.Validate(false) == nil {
			t.Fatalf("%s: unsafe entry accepted", name)
		}
	}
	redacted := string(Bytes(base.Redacted()))
	if strings.Contains(redacted, "alice-secret") || !strings.Contains(redacted, `"passwordSet": true`) {
		t.Fatal("entry password leaked")
	}
	merged, e := Parse([]byte(redacted), &base, false)
	if e != nil || !merged.Equal(base) {
		t.Fatalf("entry password merge: %v", e)
	}
	renamed := strings.Replace(redacted, `"name": "office"`, `"name": "branch"`, 1)
	if _, e := Parse([]byte(renamed), &base, false); e == nil {
		t.Fatal("renamed entry kept the old password")
	}
	noEntries, e := Parse(Bytes(fixture()), nil, false)
	if e != nil || noEntries.Entries != nil || strings.Contains(string(Bytes(noEntries)), "entries") {
		t.Fatal("entries must stay optional")
	}
	extra := strings.Replace(string(Bytes(base)), `"profile": "mixed"`, `"profile": "mixed", "extra": 1`, 1)
	if _, e := Parse([]byte(extra), nil, false); e == nil {
		t.Fatal("unknown entry field accepted")
	}
	static := base.Clone()
	static.Entries[0].Profile = "direct"
	if string(Bytes(static.Static())) != string(Bytes(base.Static())) {
		t.Fatal("profile rebinding must not be static")
	}
	static.Entries[0].Users[0].Password = "changed"
	if string(Bytes(static.Static())) == string(Bytes(base.Static())) {
		t.Fatal("entry user change must be static")
	}
}
func TestExampleProfiles(t *testing.T) {
	b, e := os.ReadFile("../../examples/profiles.json")
	if e != nil {
		t.Fatal(e)
	}
	c, e := Parse(b, nil, true)
	if e != nil || len(c.Entries) != 1 {
		t.Fatalf("example: %v", e)
	}
}
