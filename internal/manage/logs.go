package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShaohongDong/relayharbor/internal/config"
)

const MaxLogBytes = 256 << 10

type LogEntry struct {
	Time     string `json:"time"`
	Unit     string `json:"unit"`
	Priority string `json:"priority"`
	Message  string `json:"message"`
}
type LogResult struct {
	Entries   []LogEntry `json:"entries"`
	Truncated bool       `json:"truncated"`
}
type cappedLog struct {
	bytes.Buffer
	truncated bool
}

func (b *cappedLog) Write(p []byte) (int, error) {
	n := len(p)
	left := MaxLogBytes - b.Len()
	if n > left {
		b.truncated = true
		p = p[:left]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func journal(ctx context.Context, limit int) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, "journalctl", "-u", "gost.service", "-u", "gost-deploy-api.service", "--no-pager", "--all", "-o", "json", "-n", strconv.Itoa(limit))
	b := &cappedLog{}
	cmd.Stdout = b
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, false, errors.New("无法读取管理日志")
	}
	return b.Bytes(), b.truncated, nil
}

var authHeader = regexp.MustCompile(`(?i)(bearer|basic)\s+[a-z0-9+/=_-]+`)
var passwordField = regexp.MustCompile(`(?i)("?(?:password|token)"?\s*[:=]\s*)("[^"\r\n]*"|[^\s,}]+)`)

// Logs only reads the two managed journals, never worker files or user paths.
func (m *Manager) Logs(ctx context.Context, s *State, c config.Config, limit int) (LogResult, error) {
	out := LogResult{Entries: []LogEntry{}}
	if limit < 1 || limit > 500 {
		return out, errors.New("日志条数必须为 1–500")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	read := m.Journal
	if read == nil {
		read = journal
	}
	b, truncated, err := read(ctx, limit)
	if err != nil {
		return out, errors.New("无法读取管理日志")
	}
	if len(b) > MaxLogBytes {
		b = b[:MaxLogBytes]
		truncated = true
	}
	secrets := []string{s.Runtime.Auth.Password}
	if token, e := os.ReadFile(m.P.Token); e == nil {
		secrets = append(secrets, strings.TrimSpace(string(token)))
	}
	add := func(cfg config.Config) {
		for _, u := range cfg.Listener.Users {
			secrets = append(secrets, u.Password)
		}
		for _, e := range cfg.Entries {
			for _, u := range e.Users {
				secrets = append(secrets, u.Password)
			}
		}
		for _, p := range cfg.Profiles {
			for _, n := range p.Nodes {
				if n.Auth != nil {
					secrets = append(secrets, n.Auth.Password)
				}
			}
		}
	}
	add(c)
	if s.Previous != nil {
		add(s.Previous.Config)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	// Only decoded messages are returned; incomplete or malformed records are discarded.
	redact := func(v string) string {
		v = authHeader.ReplaceAllString(v, "[REDACTED]")
		v = passwordField.ReplaceAllString(v, "${1}[REDACTED]")
		for _, secret := range secrets {
			if secret != "" {
				v = strings.ReplaceAll(v, secret, "[REDACTED]")
			}
		}
		return v
	}
	used := 0
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row map[string]any
		if json.Unmarshal(line, &row) != nil {
			truncated = true
			continue
		}
		message, ok := row["MESSAGE"].(string)
		if !ok {
			continue
		}
		unit, _ := row["_SYSTEMD_UNIT"].(string)
		if unit != "gost.service" && unit != "gost-deploy-api.service" {
			continue
		}
		timestamp, _ := row["__REALTIME_TIMESTAMP"].(string)
		micros, _ := strconv.ParseInt(timestamp, 10, 64)
		stamp := ""
		if micros > 0 {
			stamp = time.UnixMicro(micros).UTC().Format(time.RFC3339Nano)
		}
		priority, _ := row["PRIORITY"].(string)
		if len(priority) != 1 || priority[0] < '0' || priority[0] > '7' {
			priority = ""
		}
		message = redact(message)
		if len(message) > 4096 {
			message = string([]rune(message)[:min(1024, len([]rune(message)))])
			truncated = true
		}
		entry := LogEntry{stamp, unit, priority, message}
		// Account for indentation in the public JSON envelope as well as message bytes.
		encoded, _ := json.MarshalIndent(entry, "      ", "  ")
		cost := len(encoded) + 8
		if len(out.Entries) >= limit || used+cost > MaxLogBytes-1024 {
			truncated = true
			break
		}
		used += cost
		out.Entries = append(out.Entries, entry)
	}
	out.Truncated = truncated
	return out, nil
}
