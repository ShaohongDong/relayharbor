package subscription

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaohongDong/relayharbor/internal/config"
)

func TestExportIsolation(t *testing.T) {
	u := User{ID: "phone", Name: "设备: \"#one\"", Port: 8443, Enabled: true, Password: "secret:#\"\\", Token: config.Secret()}
	if e := u.Validate(); e != nil {
		t.Fatal(e)
	}
	full, provider := string(YAML("panel.example.com", u, false)), string(YAML("panel.example.com", u, true))
	for _, s := range []string{full, provider} {
		if !strings.Contains(s, `password: "secret:#\"\\"`) {
			t.Fatal("credential must be a quoted scalar", s)
		}
		if strings.Contains(s, u.Token) || strings.Contains(s, "DIRECT") || !strings.Contains(s, "skip-cert-verify: false") || !strings.Contains(s, "udp: false") {
			t.Fatal("export contract")
		}
	}
	if !strings.Contains(full, "MATCH,GOST-出口") || strings.Contains(provider, "rules:") {
		t.Fatal("provider/full formats")
	}
	native, _ := json.Marshal(Native(u, "127.0.0.1:12345", config.Auth{Username: "internal", Password: "upstream-secret"}, "/private/cert", "/private/key"))
	if strings.Contains(string(native), `"type":"direct"`) || !strings.Contains(string(native), `"action":"reject","network":"udp"`) {
		t.Fatal("worker must fail closed and reject UDP", string(native))
	}
	for _, port := range []int{0, 443, 65536} {
		u.Port = port
		if u.Validate() == nil {
			t.Fatal("invalid port", port)
		}
	}
}
