package manage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrSetupRevision = errors.New("配置已变化，请刷新后重试")
var ErrSetupBusy = errors.New("部署任务正在进行")
var ErrSetupCompleted = errors.New("部署设置已完成，请使用服务器 CLI")
var ErrAnyTLSConfigured = errors.New("AnyTLS 已初始化")

type SetupJob struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Step        string     `json:"step"`
	Error       string     `json:"error,omitempty"`
	Started     time.Time  `json:"startedAt"`
	Finished    *time.Time `json:"finishedAt,omitempty"`
	RequestHash string     `json:"requestHash"`
	Revision    string     `json:"revision"`
	Domain      string     `json:"domain,omitempty"`
	Email       string     `json:"email,omitempty"`
	AcceptTOS   bool       `json:"acceptTos,omitempty"`
}

func (m *Manager) jobsDir() string { return filepath.Join(filepath.Dir(m.P.State), "setup-jobs") }
func (m *Manager) jobLock(fn func() error) error {
	if e := os.MkdirAll(m.jobsDir(), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(m.jobsDir(), "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
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
func (m *Manager) ReadSetupJob(id string) (v SetupJob, e error) {
	if !validJobID(id) {
		return v, errors.New("任务 ID 无效")
	}
	b, e := os.ReadFile(filepath.Join(m.jobsDir(), id+".json"))
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(b, &v)
	return
}
func (m *Manager) saveJob(j SetupJob) error {
	return writeJSON(filepath.Join(m.jobsDir(), j.ID+".json"), j, 0600, -1)
}
func (m *Manager) LatestSetupJob() (*SetupJob, error) {
	b, e := os.ReadFile(filepath.Join(m.jobsDir(), "latest"))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	j, e := m.ReadSetupJob(string(b))
	return &j, e
}
func (m *Manager) SetupBusy() error {
	j, e := m.LatestSetupJob()
	if e != nil {
		return e
	}
	if j != nil && (j.Status == "queued" || j.Status == "running") && m.SetupJob != j.ID {
		return ErrSetupBusy
	}
	return nil
}

// SetupOpen reports ErrSetupCompleted once the browser setup wizard has been finished.
func (m *Manager) SetupOpen() error {
	v, e := m.Access()
	if e != nil {
		return e
	}
	if v.Completed {
		return ErrSetupCompleted
	}
	return nil
}

// Reconcile is called once on API startup. systemd owns task lifetime across API restarts.
func (m *Manager) ReconcileSetup(ctx context.Context) error {
	return m.jobLock(func() error {
		j, e := m.LatestSetupJob()
		if e != nil || j == nil {
			return e
		}
		if j.Status != "queued" && j.Status != "running" {
			return nil
		}
		if time.Since(j.Started) < 30*time.Second {
			return nil
		}
		b, e := m.sys(ctx, "show", "gost-deploy-setup-"+j.ID+".service", "-p", "ActiveState", "--value")
		if e == nil && (strings.TrimSpace(string(b)) == "active" || strings.TrimSpace(string(b)) == "activating") {
			return nil
		}
		now := time.Now()
		j.Status = "interrupted"
		j.Error = "部署进程已停止，请重试"
		j.Finished = &now
		return m.saveJob(*j)
	})
}
func (m *Manager) StartSetup(ctx context.Context, kind, key, expected string, o TLSOptions) (job SetupJob, err error) {
	if kind != "tls" && kind != "anytls" {
		return job, errors.New("部署类型无效")
	}
	if len(key) < 16 || len(key) > 128 {
		return job, errors.New("需要 16–128 字符的 Idempotency-Key")
	}
	if kind == "tls" {
		if e := ValidateTLSOptions(o); e != nil {
			return job, e
		}
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + key))
	job.ID = hex.EncodeToString(sum[:16])
	payload, _ := json.Marshal(struct {
		Kind, Revision string
		Options        TLSOptions
	}{kind, expected, o})
	hash := sha256.Sum256(payload)
	err = m.jobLock(func() error {
		if old, e := m.ReadSetupJob(job.ID); e == nil {
			if old.RequestHash != hex.EncodeToString(hash[:]) {
				return ErrSetupRevision
			}
			job = old
			return nil
		} else if !os.IsNotExist(e) {
			return e
		}
		if e := m.SetupBusy(); e != nil {
			return e
		}
		if e := m.Locked(func() error {
			s, e := m.Load(false)
			if e != nil {
				return e
			}
			if s.Revision != expected {
				return ErrSetupRevision
			}
			// The wizard closes once completed; AnyTLS may still be initialized once.
			if kind == "tls" {
				if e := m.SetupOpen(); e != nil {
					return e
				}
			} else if a, e := m.loadAnyTLS(); e != nil {
				return e
			} else if a != nil {
				return ErrAnyTLSConfigured
			}
			return m.Managed(ctx, s)
		}); e != nil {
			return e
		}
		job = SetupJob{ID: job.ID, Kind: kind, Status: "queued", Step: "等待后台部署", Started: time.Now().UTC(), RequestHash: hex.EncodeToString(hash[:]), Revision: expected, Domain: o.Domain, Email: o.Email, AcceptTOS: o.AcceptTOS}
		if e := m.saveJob(job); e != nil {
			return e
		}
		if e := Atomic(filepath.Join(m.jobsDir(), "latest"), []byte(job.ID), 0600, -1); e != nil {
			return e
		}
		_, e := m.Exec(ctx, "systemd-run", "--unit=gost-deploy-setup-"+job.ID, "--collect", "--property=Type=exec", "--property=UMask=0077", "--property=RuntimeMaxSec=900", "--property=StandardOutput=null", "--property=StandardError=null", m.P.Control, "_setup", job.ID)
		if e != nil {
			now := time.Now()
			job.Status = "failed"
			job.Error = "无法启动部署任务"
			job.Finished = &now
			_ = m.saveJob(job)
		}
		return e
	})
	return
}
func (m *Manager) RunSetup(ctx context.Context, id string) (err error) {
	var j SetupJob
	if err = m.jobLock(func() error {
		var e error
		j, e = m.ReadSetupJob(id)
		if e != nil {
			return e
		}
		if j.Status != "queued" {
			return errors.New("任务已运行")
		}
		j.Status = "running"
		j.Step = "下载校验并配置服务"
		return m.saveJob(j)
	}); err != nil {
		return err
	}
	m.SetupJob = id
	defer func() {
		now := time.Now().UTC()
		j.Finished = &now
		j.Status = "succeeded"
		j.Step = "部署完成"
		if err != nil {
			j.Status = "failed"
			j.Step = "部署失败，可重试"
			j.Error = err.Error()
		}
		writeErr := m.jobLock(func() error { return m.saveJob(j) })
		if err == nil {
			err = writeErr
		}
	}()
	s, e := m.Load(false)
	if e != nil {
		return e
	}
	if s.Revision != j.Revision {
		return ErrSetupRevision
	}
	if j.Kind == "tls" {
		if e = m.TLSSetup(ctx, TLSOptions{Domain: j.Domain, Email: j.Email, AcceptTOS: j.AcceptTOS}); e != nil {
			return e
		}
		return m.AccessHandoff()
	}
	return m.AnyTLSSetup(ctx, "")
}
