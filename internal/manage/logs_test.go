package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"gitee.com/kenny3755/gost_deploy/internal/config"
)

func TestBoundedRedactedJournal(t *testing.T) {
	m := fixtureManager(t)
	c := config.Initial("127.0.0.1:1080", false)
	c.Listener.Users[0].Password = "entry-secret"
	old := c.Clone()
	old.Listener.Users[0].Password = "old-secret"
	s := &State{Previous: &Snapshot{Config: old}}
	s.Runtime.Auth.Password = "private-secret"
	os.MkdirAll(m.P.Token[:strings.LastIndex(m.P.Token, "/")], 0700)
	os.WriteFile(m.P.Token, []byte("token-secret\n"), 0600)
	m.Journal = func(_ context.Context, limit int) ([]byte, bool, error) {
		if limit != 100 {
			t.Fatal("limit")
		}
		row := map[string]any{"_SYSTEMD_UNIT": "gost.service", "MESSAGE": "entry-secret old-secret private-secret token-secret Bearer arbitrary-token password=other-secret <script>", "__REALTIME_TIMESTAMP": "1000000", "PRIORITY": "3"}
		b, _ := json.Marshal(row)
		return append(b, '\n'), false, nil
	}
	out, e := m.Logs(context.Background(), s, c, 100)
	if e != nil || len(out.Entries) != 1 || out.Entries[0].Time != "1970-01-01T00:00:01Z" {
		t.Fatal(out, e)
	}
	text := out.Entries[0].Message
	for _, secret := range []string{"entry-secret", "old-secret", "private-secret", "token-secret", "arbitrary-token", "other-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if !strings.Contains(text, "<script>") {
		t.Fatal("plain text should be escaped by JSON/DOM, not silently modified")
	}
	for _, limit := range []int{0, 501} {
		if _, e = m.Logs(context.Background(), s, c, limit); e == nil {
			t.Fatal("limit accepted")
		}
	}
	m.Journal = func(context.Context, int) ([]byte, bool, error) {
		return []byte(strings.Repeat("x", MaxLogBytes+1)), false, nil
	}
	out, e = m.Logs(context.Background(), s, c, 100)
	if e != nil || !out.Truncated || len(out.Entries) != 0 {
		t.Fatal("bounded malformed output", out, e)
	}
	m.Journal = func(context.Context, int) ([]byte, bool, error) { return nil, false, errors.New("secret diagnostic") }
	if _, e = m.Logs(context.Background(), s, c, 100); e == nil || strings.Contains(e.Error(), "secret diagnostic") {
		t.Fatal("journal error leaks")
	}
}
func TestJournalAllowlistAndLineLimits(t *testing.T) {
	m := fixtureManager(t)
	c := config.Initial("127.0.0.1:1080", false)
	m.Journal = func(context.Context, int) ([]byte, bool, error) {
		var data []byte
		for _, unit := range []string{"ssh.service", "gost.service", "gost-deploy-api.service"} {
			b, _ := json.Marshal(map[string]string{"_SYSTEMD_UNIT": unit, "MESSAGE": strings.Repeat("x", 10000)})
			data = append(data, append(b, '\n')...)
		}
		return data, false, nil
	}
	out, e := m.Logs(context.Background(), &State{}, c, 1)
	if e != nil || !out.Truncated || len(out.Entries) != 1 || out.Entries[0].Unit != "gost.service" || len(out.Entries[0].Message) > 4096 {
		t.Fatal("allowlist/limit", out, e)
	}
	b := &cappedLog{}
	p := []byte(strings.Repeat("x", MaxLogBytes+100))
	n, e := b.Write(p)
	if n != len(p) || e != nil || b.Len() != MaxLogBytes || !b.truncated {
		t.Fatal("unbounded journal writer")
	}
}

func TestJournalEnvelopeRespectsByteLimit(t *testing.T) {
	m := fixtureManager(t)
	c := config.Initial("127.0.0.1:1080", false)
	s := &State{Revision: strings.Repeat("r", 44)}
	m.Journal = func(context.Context, int) ([]byte, bool, error) {
		var b []byte
		for i := 0; i < 500; i++ {
			line, _ := json.Marshal(map[string]string{"_SYSTEMD_UNIT": "gost.service", "MESSAGE": strings.Repeat("<😀>\\", 300), "PRIORITY": "6"})
			b = append(b, append(line, '\n')...)
		}
		return b, false, nil
	}
	out, e := m.Logs(context.Background(), s, c, 500)
	if e != nil {
		t.Fatal(e)
	}
	body := config.Bytes(map[string]any{"data": out, "revision": s.Revision})
	if len(body) > MaxLogBytes || !out.Truncated {
		t.Fatalf("log envelope %d bytes, truncated=%v", len(body), out.Truncated)
	}
}
