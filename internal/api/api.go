// Package api exposes the authenticated, revision-checked local management API.
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"gitee.com/kenny3755/gost_deploy/internal/config"
	"gitee.com/kenny3755/gost_deploy/internal/manage"
	"gitee.com/kenny3755/gost_deploy/internal/supervisor"
	"gitee.com/kenny3755/gost_deploy/internal/webui"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const Address = "127.0.0.1:18080"
const Prefix = "/api/v1"
const MaxBody = 1048576

type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string                { return e.Message }
func fail(status int, code, msg string) error { return &Error{status, code, msg} }

type Backend interface {
	Locked(func() error) error
	Load(bool) (*manage.State, error)
	Managed(context.Context, *manage.State) error
	ReadConfig() (config.Config, error)
	Active(context.Context) bool
	Status(context.Context, *manage.State) (map[string]any, error)
	Preflight(config.Config, *config.Config) error
	Apply(context.Context, *manage.State, manage.Release, config.Config, *manage.Snapshot, bool) error
	Runtime(context.Context, supervisor.Request, any) error
	Service(context.Context, string) error
	Rollback(context.Context, *manage.State) error
	Logs(context.Context, *manage.State, config.Config, int) (manage.LogResult, error)
	TLSStatus(context.Context) (manage.TLSStatus, error)
	TLSStart(context.Context, string) error
}
type Handler struct {
	Backend Backend
	Token   string
}

func reply(w http.ResponseWriter, status int, v any, rev string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if rev != "" {
		w.Header().Set("ETag", `"`+rev+`"`)
	}
	w.WriteHeader(status)
	_, _ = w.Write(config.Bytes(v))
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/sub/") {
		h.subscription(w, r)
		return
	}
	if r.Method == http.MethodOptions {
		reply(w, 405, map[string]any{"error": map[string]string{"code": "method_not_allowed", "message": "跨域访问未启用"}}, "")
		return
	}
	data, rev, e := h.dispatch(r)
	if e != nil {
		status, code, msg := 400, "operation_failed", e.Error()
		var ae *Error
		if errors.As(e, &ae) {
			status, code, msg = ae.Status, ae.Code, ae.Message
		}
		reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}}, "")
		return
	}
	reply(w, 200, map[string]any{"data": data, "revision": rev}, rev)
}
func (h *Handler) dispatch(r *http.Request) (any, string, error) {
	if h.Token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.Token)) != 1 {
		return nil, "", fail(401, "unauthorized", "需要有效的 Bearer token")
	}
	if r.URL.Fragment != "" || !strings.HasPrefix(r.URL.Path, Prefix+"/") {
		return nil, "", fail(404, "not_found", "接口不存在")
	}
	path := strings.TrimPrefix(r.URL.Path, Prefix)
	limit := 100
	if r.URL.RawQuery != "" {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if path != "/logs" || r.Method != "GET" || err != nil || len(q) != 1 || len(q["limit"]) != 1 {
			return nil, "", fail(404, "not_found", "接口不存在")
		}
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 500 {
			return nil, "", fail(400, "invalid_request", "日志条数必须为 1–500")
		}
	}
	var body []byte
	if r.Method == "POST" || r.Method == "PUT" {
		if len(r.TransferEncoding) > 0 || r.ContentLength < 0 {
			return nil, "", fail(400, "invalid_request", "需要唯一 Content-Length")
		}
		if r.ContentLength <= 0 || r.ContentLength > MaxBody {
			return nil, "", fail(413, "request_too_large", "请求大小必须为 1–1048576 字节")
		}
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			return nil, "", fail(415, "unsupported_media_type", "仅支持 application/json")
		}
		body, e = io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
		if e != nil || int64(len(body)) != r.ContentLength {
			return nil, "", fail(400, "invalid_request", "请求不完整")
		}
		if _, e = config.DecodeJSON(body); e != nil {
			return nil, "", fail(400, "invalid_request", "请求不是有效 JSON")
		}
	}
	if path == "/anytls" || strings.HasPrefix(path, "/anytls/") {
		return h.anyTLS(r, path, body)
	}
	var data any
	rev := ""
	e := h.Backend.Locked(func() error {
		s, e := h.Backend.Load(false)
		if e != nil {
			return e
		}
		if e = h.Backend.Managed(r.Context(), s); e != nil {
			return e
		}
		c, e := h.Backend.ReadConfig()
		if e != nil {
			return e
		}
		rev = s.Revision
		if r.Method == "POST" && (path == "/rollback" || path == "/service/start" || path == "/service/stop" || path == "/service/restart" || path == "/tls/renew" || path == "/tls/reload") {
			if r.Header.Get("If-Match") == "" {
				return fail(428, "revision_required", "写操作需要 If-Match revision")
			}
			if r.Header.Get("If-Match") != `"`+rev+`"` {
				return fail(409, "revision_conflict", "配置已变化，请重新读取后更新")
			}
		}
		switch {
		case r.Method == "GET" && path == "/tls":
			data, e = h.Backend.TLSStatus(r.Context())
		case r.Method == "POST" && (path == "/tls/renew" || path == "/tls/reload"):
			v, _ := config.DecodeJSON(body)
			obj, ok := v.(map[string]any)
			if !ok || len(obj) != 0 {
				return fail(400, "invalid_request", "操作请求必须为 {}")
			}
			op := strings.TrimPrefix(path, "/tls/")
			e = h.Backend.TLSStart(r.Context(), op)
			if errors.Is(e, manage.ErrTLSBusy) {
				return fail(409, "tls_busy", e.Error())
			}
			data = map[string]any{"operation": op, "state": "queued"}
		case r.Method == "GET" && path == "/config":
			data = c.Redacted()
		case r.Method == "GET" && path == "/status":
			data, e = h.Backend.Status(r.Context(), s)
		case r.Method == "GET" && path == "/logs":
			data, e = h.Backend.Logs(r.Context(), s, c, limit)
		case r.Method == "POST" && (path == "/rollback" || path == "/service/start" || path == "/service/stop" || path == "/service/restart"):
			v, _ := config.DecodeJSON(body)
			obj, ok := v.(map[string]any)
			if !ok || len(obj) != 0 {
				return fail(400, "invalid_request", "操作请求必须为 {}")
			}
			if path == "/rollback" {
				if s.Previous == nil {
					return fail(409, "rollback_unavailable", "没有上一份可恢复版本")
				}
				e = h.Backend.Rollback(r.Context(), s)
			} else {
				e = h.Backend.Service(r.Context(), strings.TrimPrefix(path, "/service/"))
			}
			if e != nil {
				return e
			}
			s, e = h.Backend.Load(false)
			if e != nil {
				return e
			}
			rev = s.Revision
			data, e = h.Backend.Status(r.Context(), s)
		case r.Method == "GET" && (path == "/nodes" || path == "/profiles"):
			nodes := supervisor.Nodes(c)
			if h.Backend.Active(r.Context()) {
				var status supervisor.Status
				e = h.Backend.Runtime(r.Context(), supervisor.Request{Command: "status"}, &status)
				if e != nil {
					return e
				}
				nodes = status.Nodes
			}
			if path == "/nodes" {
				data = nodes
			} else {
				profiles := []any{}
				for _, p := range c.Profiles {
					ns := []supervisor.NodeStatus{}
					for _, n := range nodes {
						if n.Profile == p.Name {
							ns = append(ns, n)
						}
					}
					profiles = append(profiles, map[string]any{"name": p.Name, "type": p.Type, "active": p.Name == c.ActiveProfile, "nodes": ns})
				}
				data = profiles
			}
		case r.Method == "POST" && path == "/config/validate":
			_, e = config.Parse(body, &c, false)
			data = map[string]bool{"valid": true, "runtimeValidated": false}
		case r.Method == "POST" && strings.HasPrefix(path, "/profiles/") && strings.HasSuffix(path, "/probe"):
			name := strings.TrimSuffix(strings.TrimPrefix(path, "/profiles/"), "/probe")
			if !config.Identifier(name) {
				return config.ErrInvalid
			}
			found := false
			for _, p := range c.Profiles {
				if p.Name == name {
					found = true
				}
			}
			if !found {
				return fail(404, "profile_not_found", "profile 不存在")
			}
			if !h.Backend.Active(r.Context()) {
				return fail(409, "service_stopped", "服务停止时无法探测")
			}
			var nodes []supervisor.NodeStatus
			e = h.Backend.Runtime(r.Context(), supervisor.Request{Command: "probe", Profile: name}, &nodes)
			data = nodes
		case r.Method == "PUT" && (path == "/config" || path == "/active-profile"):
			expected := r.Header.Get("If-Match")
			if expected == "" {
				return fail(428, "revision_required", "写操作需要 If-Match revision")
			}
			if expected != `"`+rev+`"` {
				return fail(409, "revision_conflict", "配置已变化，请重新读取后更新")
			}
			var candidate config.Config
			if path == "/config" {
				candidate, e = config.Parse(body, &c, false)
			} else {
				v, err := config.DecodeJSON(body)
				if err != nil {
					return err
				}
				m, ok := v.(map[string]any)
				if !ok || len(m) != 1 {
					return config.ErrInvalid
				}
				name, ok := m["name"].(string)
				if !ok {
					return config.ErrInvalid
				}
				candidate = c.Clone()
				candidate.ActiveProfile = name
				e = candidate.Validate(false)
			}
			if e != nil {
				return e
			}
			if !candidate.Equal(c) {
				if e = h.Backend.Preflight(candidate, &c); e != nil {
					return e
				}
				if e = h.Backend.Apply(r.Context(), s, s.Current, candidate, nil, false); e != nil {
					return e
				}
			}
			s, e = h.Backend.Load(false)
			if e != nil {
				return e
			}
			rev = s.Revision
			data, e = h.Backend.Status(r.Context(), s)
		default:
			return fail(404, "not_found", "接口不存在")
		}
		return e
	})
	return data, rev, e
}
func Serve(ctx context.Context, m *manage.Manager) error {
	b, e := os.ReadFile(m.P.Token)
	if e != nil {
		return e
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return errors.New("API token 不能为空")
	}
	l, e := net.Listen("tcp", Address)
	if e != nil {
		return e
	}
	return ServeListener(ctx, l, webui.Handler(&Handler{m, token}))
}
func ServeListener(ctx context.Context, l net.Listener, h http.Handler) error {
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Shutdown(context.Background())
		case <-done:
		}
	}()
	e := server.Serve(l)
	if ctx.Err() != nil {
		_ = server.Shutdown(context.Background())
	}
	close(done)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func Client(ctx context.Context, tokenPath, method, path string, value any, rev string) (json.RawMessage, string, error) {
	b, e := os.ReadFile(tokenPath)
	if e != nil {
		return nil, "", errors.New("无法读取管理 token")
	}
	var body io.Reader
	if value != nil {
		if raw, ok := value.(json.RawMessage); ok {
			body = bytes.NewReader(raw)
		} else {
			body = bytes.NewReader(config.Bytes(value))
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://"+Address+Prefix+path, body)
	if e != nil {
		return nil, "", errors.New("请求无效")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if rev != "" {
		req.Header.Set("If-Match", `"`+rev+`"`)
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 180 * time.Second, Transport: transport}
	resp, e := client.Do(req)
	if e != nil {
		return nil, "", errors.New("无法联系本机管理 API")
	}
	defer resp.Body.Close()
	var result struct {
		Data     json.RawMessage `json:"data"`
		Revision string          `json:"revision"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, MaxBody+1)).Decode(&result)
	if e != nil {
		return nil, "", errors.New("API 响应无效")
	}
	if resp.StatusCode != 200 {
		return nil, "", errors.New(result.Error.Message)
	}
	return result.Data, result.Revision, nil
}
func ProfilePath(name string) string { return "/profiles/" + url.PathEscape(name) + "/probe" }
