package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/manage"
)

type anyTLSBackend interface {
	AnyTLS(context.Context, string, manage.AnyTLSRequest, string) (any, string, error)
	Subscription(context.Context, string, bool) ([]byte, error)
}

func (h *Handler) anyTLS(r *http.Request, path string, body []byte) (any, string, error) {
	b, ok := h.Backend.(anyTLSBackend)
	if !ok {
		return nil, "", fail(503, "unavailable", "AnyTLS 不可用")
	}
	op, id := "", ""
	if path == "/anytls" && r.Method == "GET" {
		op = "status"
	} else if r.Method == "POST" {
		parts := strings.Split(strings.TrimPrefix(path, "/anytls/"), "/")
		switch {
		case len(parts) == 1 && (parts[0] == "enable" || parts[0] == "disable"):
			op = parts[0]
		case len(parts) == 1 && parts[0] == "users":
			op = "add"
		case len(parts) == 3 && parts[0] == "users" && config.Identifier(parts[1]):
			id, op = parts[1], parts[2]
			switch op {
			case "enable", "disable":
				op = "user-" + op
			case "rename", "delete", "reset-token", "reset-password", "credentials":
			default:
				op = ""
			}
		}
	}
	if op == "" {
		return nil, "", fail(404, "not_found", "接口不存在")
	}
	var req manage.AnyTLSRequest
	if op != "status" {
		match := r.Header.Get("If-Match")
		if match == "" {
			return nil, "", fail(428, "revision_required", "操作需要 AnyTLS If-Match revision")
		}
		if len(match) < 3 || match[0] != '"' || match[len(match)-1] != '"' {
			return nil, "", fail(409, "revision_conflict", "AnyTLS revision 无效")
		}
		// Each operation has an explicit request shape.
		v, _ := config.DecodeJSON(body)
		obj, valid := v.(map[string]any)
		if !valid {
			return nil, "", fail(400, "invalid_request", "需要 JSON 对象")
		}
		for key, value := range obj {
			if value == nil {
				return nil, "", fail(400, "invalid_request", "字段不能为 null")
			}
			if !(op == "add" && (key == "id" || key == "name" || key == "port")) && !(op == "rename" && key == "name") {
				return nil, "", fail(400, "invalid_request", "操作包含不支持的字段")
			}
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if e := d.Decode(&req); e != nil {
			return nil, "", fail(400, "invalid_request", "请求字段无效")
		}
		if op != "add" {
			req.ID = id
		}
	}
	expected := strings.Trim(r.Header.Get("If-Match"), "\"")
	data, rev, e := b.AnyTLS(r.Context(), op, req, expected)
	if errors.Is(e, manage.ErrAnyTLSRevision) {
		e = fail(409, "revision_conflict", e.Error())
	} else if errors.Is(e, manage.ErrTLSBusy) {
		e = fail(409, "tls_busy", e.Error())
	} else if errors.Is(e, manage.ErrSubscription) {
		e = fail(404, "not_found", "用户未启用或不存在")
	}
	return data, rev, e
}

func (h *Handler) subscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	// Only the loopback Nginx listener is trusted to assert HTTPS.
	ip := net.ParseIP(host)
	if r.TLS == nil && (ip == nil || !ip.IsLoopback() || r.Header.Get("X-Forwarded-Proto") != "https") {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sub/"), "/")
	if r.Method != "GET" || r.URL.RawQuery != "" || r.URL.RawPath != "" || len(parts) != 2 || len(parts[0]) != 43 ||
		(parts[1] != "clash.yaml" && parts[1] != "proxies.yaml") {
		http.NotFound(w, r)
		return
	}
	b, ok := h.Backend.(anyTLSBackend)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, e := b.Subscription(r.Context(), parts[0], parts[1] == "proxies.yaml")
	if e != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+parts[1]+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
