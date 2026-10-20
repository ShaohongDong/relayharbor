package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type localKey struct{}

// Local is set only by the root-checked Unix socket listener, never by headers.
func Local(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), localKey{}, true))
}
func IsLocal(r *http.Request) bool { v, _ := r.Context().Value(localKey{}).(bool); return v }

type session struct {
	Generation, CSRF string
	Created, Seen    time.Time
}
type attempt struct {
	Start time.Time
	Count int
}
type Service struct {
	Store    *Store
	mu       sync.Mutex
	sessions map[string]session
	attempts map[string]attempt
	work     chan struct{}
	// Origin validates exact configured HTTPS origins; forwarded headers alone do not grant trust.
	Origin func(*http.Request) string
}

func New(s *Store) *Service {
	return &Service{Store: s, sessions: map[string]session{}, attempts: map[string]attempt{}, work: make(chan struct{}, 2)}
}
func (a *Service) origin(r *http.Request) string {
	if a.Origin != nil {
		return a.Origin(r)
	}
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return ""
}
func (a *Service) sameOrigin(r *http.Request) bool {
	o := a.origin(r)
	return o != "" && r.Header.Get("Origin") == o
}

const cookieName = "__Host-relayharbor"

func (a *Service) Check(r *http.Request) (string, bool) {
	if IsLocal(r) {
		return "", true
	}
	if a.origin(r) == "" {
		return "", false
	}
	c, e := r.Cookie(cookieName)
	if e != nil {
		return "", false
	}
	v, e := a.Store.Read()
	if e != nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[digest(c.Value)]
	now := time.Now()
	if !ok || s.Generation != v.Generation || now.Sub(s.Created) > 12*time.Hour || now.Sub(s.Seen) > 30*time.Minute {
		delete(a.sessions, digest(c.Value))
		return "", false
	}
	if r.Method != "GET" && r.Method != "HEAD" && (!a.sameOrigin(r) || !equal(r.Header.Get("X-CSRF-Token"), s.CSRF)) {
		return "", false
	}
	s.Seen = now
	a.sessions[digest(c.Value)] = s
	return s.CSRF, true
}
func respond(w http.ResponseWriter, status int, data any, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if code != "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "revision": ""})
	}
}
func (a *Service) clear(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie(cookieName); e == nil {
		a.mu.Lock()
		delete(a.sessions, digest(c.Value))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func (a *Service) allowed(r *http.Request) bool {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	// The loopback reverse proxy overwrites this header. Never trust it on public listeners.
	if r.TLS == nil && net.ParseIP(ip).IsLoopback() {
		if p := net.ParseIP(r.Header.Get("X-Real-IP")); p != nil {
			ip = p.String()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.Sub(v.Start) > time.Minute {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[ip]
	if v.Start.IsZero() {
		v.Start = now
	}
	if v.Count >= 10 || len(a.attempts) > 4096 {
		return false
	}
	v.Count++
	a.attempts[ip] = v
	return true
}
func (a *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/")
	if r.URL.RawQuery != "" {
		respond(w, 404, nil, "not_found")
		return
	}
	if r.Method == "GET" && path == "status" {
		v, e := a.Store.Read()
		if e != nil {
			respond(w, 503, nil, "auth_unavailable")
			return
		}
		respond(w, 200, map[string]any{"initialized": v.Hash != ""}, "")
		return
	}
	if r.Method == "GET" && path == "session" {
		csrf, ok := a.Check(r)
		if !ok {
			respond(w, 401, nil, "unauthorized")
			return
		}
		respond(w, 200, map[string]string{"csrf": csrf}, "")
		return
	}
	if r.Method != "POST" || !(path == "initialize" || path == "login" || path == "logout" || path == "password") {
		respond(w, 404, nil, "not_found")
		return
	}
	if !a.sameOrigin(r) {
		respond(w, 403, nil, "origin_rejected")
		return
	}
	if path == "logout" || path == "password" {
		if _, ok := a.Check(r); !ok {
			respond(w, 401, nil, "unauthorized")
			return
		}
	}
	if path == "logout" {
		a.clear(w, r)
		respond(w, 200, map[string]bool{"loggedOut": true}, "")
		return
	}
	if !a.allowed(r) {
		respond(w, 429, nil, "too_many_attempts")
		return
	}
	select {
	case a.work <- struct{}{}:
		defer func() { <-a.work }()
	default:
		respond(w, 429, nil, "too_many_attempts")
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		respond(w, 415, nil, "json_required")
		return
	}
	var body struct {
		Code        string `json:"code"`
		Password    string `json:"password"`
		OldPassword string `json:"oldPassword"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if e := d.Decode(&body); e != nil {
		respond(w, 400, nil, "invalid_request")
		return
	}
	if e := d.Decode(&struct{}{}); !errors.Is(e, io.EOF) {
		respond(w, 400, nil, "invalid_request")
		return
	}
	if path == "initialize" {
		if e := a.Store.Initialize(body.Code, body.Password); e != nil {
			respond(w, 400, nil, "initialization_failed")
			return
		}
	}
	if path == "password" {
		if e := a.Store.Change(body.OldPassword, body.Password); e != nil {
			respond(w, 400, nil, "password_change_failed")
			return
		}
		a.clear(w, r)
		respond(w, 200, map[string]bool{"changed": true}, "")
		return
	}
	v, e := a.Store.Login(body.Password)
	if e != nil {
		respond(w, 401, nil, "invalid_credentials")
		return
	}
	id, csrf := Secret(), Secret()
	now := time.Now()
	a.mu.Lock()
	for k, s := range a.sessions {
		if now.Sub(s.Created) > 12*time.Hour || now.Sub(s.Seen) > 30*time.Minute || s.Generation != v.Generation {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 128 {
		a.mu.Unlock()
		respond(w, 429, nil, "too_many_sessions")
		return
	}
	a.sessions[digest(id)] = session{v.Generation, csrf, now, now}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]string{"csrf": csrf}, "")
}
func ValidOrigin(origin string) bool {
	u, e := url.Parse(origin)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.Path == "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
