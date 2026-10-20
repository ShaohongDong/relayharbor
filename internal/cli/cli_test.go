package cli

import (
	"bytes"
	"context"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitAndCheck(t *testing.T) {
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.json"), filepath.Join(dir, "two.json")
	var passwords []string
	for _, path := range []string{one, two} {
		out := &bytes.Buffer{}
		if e := Main(context.Background(), []string{"init", "--output", path}, out); e != nil {
			t.Fatal(e)
		}
		b, _ := os.ReadFile(path)
		c, e := config.Parse(b, nil, false)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(out.String(), c.Listener.Users[0].Password) {
			t.Fatal("secret output")
		}
		passwords = append(passwords, c.Listener.Users[0].Password)
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0600 {
			t.Fatal("permissions")
		}
		if e = Main(context.Background(), []string{"check", "--config", path}, out); e != nil {
			t.Fatal(e)
		}
	}
	if passwords[0] == passwords[1] {
		t.Fatal("password reused")
	}
	before, _ := os.ReadFile(one)
	if Main(context.Background(), []string{"init", "--output", one}, &bytes.Buffer{}) == nil {
		t.Fatal("overwrote file")
	}
	after, _ := os.ReadFile(one)
	if !bytes.Equal(before, after) {
		t.Fatal("changed existing file")
	}
}
func TestVersion(t *testing.T) {
	b := &bytes.Buffer{}
	if e := Main(context.Background(), []string{"version"}, b); e != nil || !strings.Contains(b.String(), "gostctl ") {
		t.Fatal(e)
	}
}
