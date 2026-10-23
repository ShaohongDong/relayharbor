package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/ShaohongDong/relayharbor/internal/manage"
)

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, Prefix)
	if path != "/setup" && path != "/setup/credentials" && path != "/setup/complete" && path != "/tls/setup" && path != "/anytls/setup" && !strings.HasPrefix(path, "/setup/jobs/") {
		return false
	}
	m, ok := h.Backend.(*manage.Manager)
	if !ok {
		reply(w, 503, map[string]any{"error": map[string]string{"code": "unavailable", "message": "部署服务不可用"}}, "")
		return true
	}
	var data any
	rev := ""
	status := 200
	err := func() error {
		if r.URL.RawQuery != "" {
			return fail(404, "not_found", "接口不存在")
		}
		if r.Method == "GET" && strings.HasPrefix(path, "/setup/jobs/") {
			j, e := m.ReadSetupJob(strings.TrimPrefix(path, "/setup/jobs/"))
			if os.IsNotExist(e) {
				return fail(404, "not_found", "任务不存在")
			}
			data = j
			return e
		}
		if r.Method == "GET" && path == "/setup" {
			s, e := m.Load(false)
			if e != nil {
				return e
			}
			rev = s.Revision
			access, e := m.Access()
			if e != nil {
				return e
			}
			j, e := m.LatestSetupJob()
			if e != nil {
				return e
			}
			data = map[string]any{"completed": access.Completed, "url": m.SetupAddress(), "temporaryURL": access.URL(), "closeAt": access.CloseAt, "job": j}
			return nil
		}
		if r.Method != "POST" || path == "/setup" || strings.HasPrefix(path, "/setup/jobs/") {
			return fail(405, "method_not_allowed", "方法不支持")
		}
		if path != "/anytls/setup" {
			if e := m.SetupOpen(); e != nil {
				return e
			}
		}
		expected := r.Header.Get("If-Match")
		if expected == "" {
			return fail(428, "revision_required", "需要 If-Match")
		}
		if len(expected) < 3 || expected[0] != '"' || expected[len(expected)-1] != '"' {
			return manage.ErrSetupRevision
		}
		expected = strings.Trim(expected, "\"")
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			return fail(415, "unsupported_media_type", "需要 JSON")
		}
		var body struct {
			Domain    string `json:"domain"`
			Email     string `json:"email"`
			AcceptTOS bool   `json:"acceptTos"`
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if e != nil {
			return fail(400, "invalid_request", "请求过大")
		}
		parsed, e := config.DecodeJSON(raw)
		if e != nil {
			return fail(400, "invalid_request", "JSON 无效")
		}
		obj, ok := parsed.(map[string]any)
		if !ok {
			return fail(400, "invalid_request", "需要 JSON 对象")
		}
		if path != "/tls/setup" && len(obj) != 0 {
			return fail(400, "invalid_request", "此操作不接受字段")
		}
		for _, v := range obj {
			if v == nil {
				return fail(400, "invalid_request", "字段不能为 null")
			}
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if e := d.Decode(&body); e != nil {
			return fail(400, "invalid_request", "请求字段无效")
		}
		if path == "/setup/credentials" {
			return m.Locked(func() error {
				s, e := m.Load(false)
				if e != nil {
					return e
				}
				if s.Revision != expected {
					return manage.ErrSetupRevision
				}
				c, e := m.ReadConfig()
				if e != nil {
					return e
				}
				data = c.Listener
				return nil
			})
		}
		if path == "/setup/complete" {
			if e := m.SetupComplete(expected); e != nil {
				return e
			}
			data = map[string]bool{"completed": true}
			return nil
		}
		kind := "anytls"
		if path == "/tls/setup" {
			kind = "tls"
		}
		job, e := m.StartSetup(r.Context(), kind, r.Header.Get("Idempotency-Key"), expected, manage.TLSOptions{Domain: body.Domain, Email: body.Email, AcceptTOS: body.AcceptTOS})
		if e != nil {
			return e
		}
		status = 202
		data = job
		return nil
	}()
	if err != nil {
		code := "operation_failed"
		status = 400
		if errors.Is(err, manage.ErrSetupRevision) {
			status = 409
			code = "revision_conflict"
		}
		if errors.Is(err, manage.ErrSetupBusy) || errors.Is(err, manage.ErrTLSBusy) {
			status = 409
			code = "setup_busy"
		}
		if errors.Is(err, manage.ErrSetupCompleted) || errors.Is(err, manage.ErrAnyTLSConfigured) {
			status = 409
			code = "setup_completed"
		}
		var ae *Error
		if errors.As(err, &ae) {
			status = ae.Status
			code = ae.Code
		}
		reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}}, "")
	} else {
		reply(w, status, map[string]any{"data": data, "revision": rev}, rev)
	}
	return true
}
