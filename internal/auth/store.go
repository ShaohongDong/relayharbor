// Package auth owns administrator passwords, bootstrap claims and browser sessions.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unicode/utf8"
)

const iterations = 600000

var ErrCredentials = errors.New("初始化码或密码无效")

type State struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
	Salt       string `json:"salt,omitempty"`
	Hash       string `json:"hash,omitempty"`
	Iterations int    `json:"iterations,omitempty"`
	Claim      string `json:"claim,omitempty"`
}
type Store struct {
	Path string
	mu   sync.Mutex
}

func Secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(s string) string {
	v := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(v[:])
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (s *Store) locked(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := os.MkdirAll(filepath.Dir(s.Path), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func (s *Store) read() (State, error) {
	var v State
	f, e := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(e) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return v, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return v, errors.New("认证文件权限无效")
	}
	e = json.NewDecoder(f).Decode(&v)
	if e == nil && (v.Version != 1 || v.Generation == "" || (v.Hash != "" && (v.Iterations != iterations || v.Salt == ""))) {
		e = errors.New("认证状态无效")
	}
	return v, e
}
func (s *Store) write(v State) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.Path), ".auth-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), s.Path)
}
func (s *Store) Read() (v State, e error) {
	e = s.locked(func() error { v, e = s.read(); return e })
	return
}

// Claim rotates an unused claim, or explicitly resets the administrator. It never
// returns an existing secret: only its digest is persisted.
func (s *Store) Claim(reset bool) (code string, e error) {
	e = s.locked(func() error {
		v, e := s.read()
		if e != nil {
			return e
		}
		if v.Hash != "" && !reset {
			return nil
		}
		code = Secret()
		return s.write(State{Version: 1, Generation: Secret(), Claim: digest(code)})
	})
	return
}
func password(v *State, p string) error {
	if !utf8.ValidString(p) || utf8.RuneCountInString(p) < 12 || utf8.RuneCountInString(p) > 128 {
		return errors.New("密码需要 12–128 个字符")
	}
	v.Salt = Secret()
	v.Iterations = iterations
	key, e := pbkdf2.Key(sha256.New, p, []byte(v.Salt), v.Iterations, 32)
	if e != nil {
		return e
	}
	v.Hash = base64.RawURLEncoding.EncodeToString(key)
	v.Claim = ""
	v.Generation = Secret()
	v.Version = 1
	return nil
}
func verify(v State, p string) bool {
	if len(p) > 512 || v.Hash == "" {
		return false
	}
	key, e := pbkdf2.Key(sha256.New, p, []byte(v.Salt), iterations, 32)
	return e == nil && equal(v.Hash, base64.RawURLEncoding.EncodeToString(key))
}
func (s *Store) Initialize(code, p string) error {
	return s.locked(func() error {
		v, e := s.read()
		if e != nil {
			return e
		}
		if v.Hash != "" || v.Claim == "" || !equal(v.Claim, digest(code)) {
			return ErrCredentials
		}
		if e = password(&v, p); e != nil {
			return e
		}
		return s.write(v)
	})
}
func (s *Store) Login(p string) (v State, e error) {
	e = s.locked(func() error {
		v, e = s.read()
		if e != nil {
			return e
		}
		if !verify(v, p) {
			return ErrCredentials
		}
		return nil
	})
	return
}
func (s *Store) Change(old, p string) error {
	return s.locked(func() error {
		v, e := s.read()
		if e != nil {
			return e
		}
		if !verify(v, old) {
			return ErrCredentials
		}
		if e = password(&v, p); e != nil {
			return e
		}
		return s.write(v)
	})
}
