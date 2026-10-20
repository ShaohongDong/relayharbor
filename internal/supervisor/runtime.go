// Package supervisor runs the unprivileged generation-based forwarding service.
package supervisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"github.com/ShaohongDong/relayharbor/internal/native"
	"github.com/ShaohongDong/relayharbor/internal/socks"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Input struct {
	Config   config.Config  `json:"config"`
	Options  native.Options `json:"options"`
	Binary   string         `json:"binary"`
	Revision string         `json:"revision"`
}
type CommandFactory func(context.Context, string, ...string) *exec.Cmd
type Worker struct {
	r         *Runtime
	node      *config.Node
	key, addr string
	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan struct{}
	refs      atomic.Int64
}

func (w *Worker) aliveLocked() bool {
	if w.done == nil {
		return false
	}
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}
func (w *Worker) start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.aliveLocked() {
		return nil
	}
	path := filepath.Join(w.r.Dir, w.key+".json")
	if e := Private(path, config.Bytes(native.Worker(w.addr, w.r.Input.Options.Auth, w.node))); e != nil {
		return e
	}
	log, e := privateFile(filepath.Join(w.r.Dir, w.key+".log"))
	if e != nil {
		return e
	}
	cmd := w.r.Command(w.r.ctx, w.r.Input.Binary, "-C", path)
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		return errors.New("GOST worker 启动失败")
	}
	w.cmd = cmd
	done := make(chan struct{})
	w.done = done
	go func() { _ = cmd.Wait(); log.Close(); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return errors.New("GOST worker 提前退出")
		case <-w.r.ctx.Done():
			return w.r.ctx.Err()
		default:
		}
		if e := checkAuth(w.r.ctx, w.addr, w.r.Input.Options.Auth); e == nil {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	<-done
	return errors.New("GOST worker 启动超时")
}
func (w *Worker) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.aliveLocked() {
		_ = w.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-w.done:
		case <-time.After(3 * time.Second):
			_ = w.cmd.Process.Kill()
			<-w.done
		}
	}
	_ = os.Remove(filepath.Join(w.r.Dir, w.key+".json"))
	_ = os.Remove(filepath.Join(w.r.Dir, w.key+".log"))
}
func (w *Worker) pid() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd == nil {
		return 0
	}
	return w.cmd.Process.Pid
}

type health struct {
	Healthy   bool
	LastProbe *float64
	LatencyMS *float64
	Failures  int
	cooldown  time.Time
	serial    uint64
}
type Generation struct {
	cfg      config.Config
	revision string
	workers  map[string]*Worker
	health   map[string]*health
	rounds   map[string]int
	next     map[string]time.Time
	pending  int
}
type NodeStatus struct {
	Profile   string   `json:"profile"`
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Addr      string   `json:"addr"`
	Healthy   *bool    `json:"healthy"`
	LastProbe *float64 `json:"lastProbe"`
	LatencyMS *float64 `json:"latencyMs"`
	Failures  int      `json:"failures"`
}
type Status struct {
	Ready         bool         `json:"ready"`
	Revision      string       `json:"revision"`
	ActiveProfile string       `json:"activeProfile"`
	Available     bool         `json:"available"`
	HealthyNodes  int          `json:"healthyNodes"`
	Nodes         []NodeStatus `json:"nodes"`
	Connections   int64        `json:"connections"`
	FrontendPID   int          `json:"frontendPid"`
}
type Runtime struct {
	Input             Input
	Dir               string
	Command           CommandFactory
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	mutation          sync.Mutex
	current, prepared *Generation
	workers           map[string]*Worker
	generations       []*Generation
	listeners         []net.Listener
	front             *exec.Cmd
	frontDone         chan struct{}
	wg                sync.WaitGroup
	probeLimit        chan struct{}
	probeWG           sync.WaitGroup
}

func New(in Input, dir string) *Runtime {
	return &Runtime{Input: in, Dir: dir, Command: exec.CommandContext, workers: map[string]*Worker{}, probeLimit: make(chan struct{}, 16)}
}
func FreeAddr() (string, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", e
	}
	defer l.Close()
	return l.Addr().String(), nil
}
func privateFile(path string) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), path), nil
}
func Private(path string, b []byte) error {
	f, e := privateFile(path)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(b)
	return e
}
func checkAuth(ctx context.Context, addr string, a config.Auth) error {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	c, e := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if e != nil {
		return e
	}
	defer c.Close()
	d, _ := ctx.Deadline()
	c.SetDeadline(d)
	return socks.Authenticate(c, a)
}
func key(profile, id string) string { return profile + "/" + id }
func (r *Runtime) getWorker(n *config.Node) (*Worker, error) {
	b := config.Bytes(n)
	h := sha256.Sum256(b)
	k := hex.EncodeToString(h[:])
	r.mu.Lock()
	w := r.workers[k]
	if w == nil {
		addr, e := FreeAddr()
		if e != nil {
			r.mu.Unlock()
			return nil, e
		}
		w = &Worker{r: r, node: n, key: k, addr: addr}
		r.workers[k] = w
	}
	r.mu.Unlock()
	return w, w.start()
}

// Prepare, Commit and Abort serialize mutations; connection handlers only hold mu briefly.
func (r *Runtime) Prepare(c config.Config, revision string) error {
	r.mutation.Lock()
	defer r.mutation.Unlock()
	return r.prepare(c, revision)
}
func (r *Runtime) prepare(c config.Config, revision string) error {
	if e := c.Validate(false); e != nil {
		return e
	}
	if revision == "" {
		return errors.New("配置代次无效")
	}
	r.mu.Lock()
	old := r.current
	r.mu.Unlock()
	if old != nil && !reflectListener(old.cfg, c) {
		return errors.New("入口变更需要事务重启")
	}
	g := &Generation{cfg: c.Clone(), revision: revision, workers: map[string]*Worker{}, health: map[string]*health{}, rounds: map[string]int{}, next: map[string]time.Time{}}
	for _, p := range g.cfg.Profiles {
		if p.Type == "direct" {
			w, e := r.getWorker(nil)
			if e != nil {
				return e
			}
			g.workers[key(p.Name, "")] = w
		} else {
			for _, n := range p.Nodes {
				n := n
				w, e := r.getWorker(&n)
				if e != nil {
					return e
				}
				k := key(p.Name, n.ID)
				g.workers[k] = w
				g.health[k] = &health{}
			}
		}
	}
	r.mu.Lock()
	if old != nil {
		for _, p := range c.Profiles {
			for _, op := range old.cfg.Profiles {
				if p.Name == op.Name && string(config.Bytes(p.Probe)) == string(config.Bytes(op.Probe)) {
					for _, n := range p.Nodes {
						k := key(p.Name, n.ID)
						if old.workers[k] == g.workers[k] && old.health[k] != nil {
							h := *old.health[k]
							g.health[k] = &h
						}
					}
				}
			}
		}
	}
	r.prepared = g
	r.generations = append(r.generations, g)
	r.mu.Unlock()
	return r.probeGeneration(g, "", true)
}
func reflectListener(a, b config.Config) bool {
	return string(config.Bytes(a.Listener)) == string(config.Bytes(b.Listener))
}
func (r *Runtime) Commit(rev string) (Status, error) {
	r.mutation.Lock()
	defer r.mutation.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prepared == nil || r.prepared.revision != rev {
		return Status{}, errors.New("待提交代次不匹配")
	}
	r.current = r.prepared
	r.prepared = nil
	return r.status(), nil
}
func (r *Runtime) Abort() {
	r.mutation.Lock()
	defer r.mutation.Unlock()
	r.mu.Lock()
	r.prepared = nil
	r.mu.Unlock()
}
func (r *Runtime) Status() Status { r.mu.Lock(); defer r.mu.Unlock(); return r.status() }
func Nodes(c config.Config) []NodeStatus {
	out := []NodeStatus{}
	for _, p := range c.Profiles {
		for _, n := range p.Nodes {
			out = append(out, NodeStatus{Profile: p.Name, ID: n.ID, Type: n.Type, Addr: n.Addr})
		}
	}
	return out
}
func (r *Runtime) status() Status {
	s := Status{Nodes: []NodeStatus{}}
	g := r.current
	if g == nil {
		return s
	}
	s.Ready = true
	s.Revision = g.revision
	s.ActiveProfile = g.cfg.ActiveProfile
	s.Available = g.cfg.Active().Type == "direct"
	for _, p := range g.cfg.Profiles {
		for _, n := range p.Nodes {
			h := g.health[key(p.Name, n.ID)]
			good := h.Healthy
			s.Nodes = append(s.Nodes, NodeStatus{p.Name, n.ID, n.Type, n.Addr, &good, h.LastProbe, h.LatencyMS, h.Failures})
			if p.Name == s.ActiveProfile && good {
				s.HealthyNodes++
				s.Available = true
			}
		}
	}
	for _, w := range r.workers {
		s.Connections += w.refs.Load()
	}
	if r.front != nil {
		s.FrontendPID = r.front.Process.Pid
	}
	return s
}
func (r *Runtime) Probe(name string) ([]NodeStatus, error) {
	r.mutation.Lock()
	defer r.mutation.Unlock()
	r.mu.Lock()
	g := r.current
	r.mu.Unlock()
	if g == nil {
		return nil, errors.New("运行时尚未就绪")
	}
	if e := r.probeGeneration(g, name, false); e != nil {
		return nil, e
	}
	return r.Status().Nodes, nil
}
func (r *Runtime) probeGeneration(g *Generation, name string, unknown bool) error {
	found := false
	var wg sync.WaitGroup
	for _, p := range g.cfg.Profiles {
		if name != "" && name != p.Name {
			continue
		}
		found = true
		r.mu.Lock()
		interval := 30 * time.Second
		if p.Probe != nil {
			interval = time.Duration(p.Probe.Interval * float64(time.Second))
		}
		g.next[p.Name] = time.Now().Add(interval)
		r.mu.Unlock()
		for _, n := range p.Nodes {
			k := key(p.Name, n.ID)
			r.mu.Lock()
			h := g.health[k]
			skip := time.Now().Before(h.cooldown) || (unknown && h.LastProbe != nil)
			serial := h.serial
			r.mu.Unlock()
			if skip {
				continue
			}
			p, n := p, n
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case r.probeLimit <- struct{}{}:
				case <-r.ctx.Done():
					return
				}
				defer func() { <-r.probeLimit }()
				w := g.workers[key(p.Name, n.ID)]
				result := socks.ProbeResult{}
				if e := w.start(); e == nil {
					result = socks.Probe(r.ctx, w.addr, r.Input.Options.Auth, *p.Probe)
				}
				r.mu.Lock()
				defer r.mu.Unlock()
				if h.serial != serial {
					return
				}
				h.serial++
				h.Healthy = result.Healthy
				h.LatencyMS = result.LatencyMS
				now := float64(time.Now().UnixMilli()) / 1000
				h.LastProbe = &now
				if result.Healthy {
					h.Failures = 0
				} else {
					h.Failures++
					h.cooldown = time.Now().Add(10 * time.Second)
				}
			}()
		}
	}
	wg.Wait()
	if !found {
		return errors.New("profile 不存在")
	}
	return nil
}
func (r *Runtime) establish(ctx context.Context, g *Generation, command byte, addr []byte) (net.Conn, []byte, *Worker, error) {
	p := g.cfg.Active()
	choices := []string{}
	r.mu.Lock()
	if command == socks.UDPTunnel {
		if p.Type == "direct" && g.cfg.Listener.UDP {
			choices = append(choices, key(p.Name, ""))
		}
	} else if command == socks.Connect {
		if p.Type == "direct" {
			choices = append(choices, key(p.Name, ""))
		} else {
			for _, n := range p.Nodes {
				k := key(p.Name, n.ID)
				if g.health[k].Healthy {
					choices = append(choices, k)
				}
			}
			if len(choices) > 0 {
				o := g.rounds[p.Name] % len(choices)
				g.rounds[p.Name]++
				choices = append(choices[o:], choices[:o]...)
			}
		}
	}
	r.mu.Unlock()
	for _, k := range choices {
		w := g.workers[k]
		w.refs.Add(1)
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		c, reply, e := socks.Dial(attempt, w.addr, r.Input.Options.Auth, command, addr)
		cancel()
		if e == nil {
			return c, reply, w, nil
		}
		w.refs.Add(-1)
		r.mu.Lock()
		if h := g.health[k]; h != nil {
			h.Healthy = false
			h.LatencyMS = nil
			h.Failures++
			h.serial++
			h.cooldown = time.Now().Add(10 * time.Second)
		}
		r.mu.Unlock()
	}
	return nil, nil, nil, errors.New("当前 profile 无可用节点")
}
func relay(dst, src net.Conn, done chan<- error) {
	_, e := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	done <- e
}
func (r *Runtime) connection(c net.Conn) {
	defer c.Close()
	stop := context.AfterFunc(r.ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	if socks.Accept(c, r.Input.Options.Auth) != nil {
		return
	}
	h := make([]byte, 3)
	if _, e := io.ReadFull(c, h); e != nil || h[0] != 5 || h[2] != 0 {
		return
	}
	addr, e := socks.ReadAddress(c)
	if e != nil {
		return
	}
	r.mu.Lock()
	g := r.current
	if g != nil {
		g.pending++
	}
	r.mu.Unlock()
	if g == nil {
		c.Write(socks.Failure)
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	up, reply, w, e := r.establish(ctx, g, h[1], addr)
	cancel()
	r.mu.Lock()
	g.pending--
	r.mu.Unlock()
	if e != nil {
		c.Write(socks.Failure)
		return
	}
	defer w.refs.Add(-1)
	defer up.Close()
	stopUp := context.AfterFunc(r.ctx, func() { up.Close() })
	defer stopUp()
	if _, e = c.Write(reply); e != nil {
		return
	}
	c.SetDeadline(time.Time{})
	done := make(chan error, 2)
	go relay(up, c, done)
	go relay(c, up, done)
	e = <-done
	if e != nil {
		up.Close()
		c.Close()
	}
	<-done
}
func (r *Runtime) accept(l net.Listener, handler func(net.Conn)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			r.wg.Add(1)
			go func() { defer r.wg.Done(); handler(c) }()
		}
	}()
}
func (r *Runtime) Start(ctx context.Context) error {
	r.ctx, r.cancel = context.WithCancel(ctx)
	if e := os.MkdirAll(r.Dir, 0700); e != nil {
		return e
	}
	if e := os.Chmod(r.Dir, 0700); e != nil {
		return e
	}
	socket := filepath.Join(r.Dir, "control.sock")
	_ = os.Remove(socket)
	l, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	os.Chmod(socket, 0600)
	r.listeners = append(r.listeners, l)
	r.accept(l, r.control)
	l, e = net.Listen("tcp", r.Input.Options.Gateway)
	if e != nil {
		return e
	}
	r.listeners = append(r.listeners, l)
	r.accept(l, r.connection)
	if e = r.Prepare(r.Input.Config, r.Input.Revision); e != nil {
		return e
	}
	if _, e = r.Commit(r.Input.Revision); e != nil {
		return e
	}
	path := filepath.Join(r.Dir, "frontend.json")
	if e = Private(path, config.Bytes(native.Frontend(r.Input.Config, r.Input.Options))); e != nil {
		return e
	}
	log, e := privateFile(filepath.Join(r.Dir, "frontend.log"))
	if e != nil {
		return e
	}
	cmd := r.Command(r.ctx, r.Input.Binary, "-C", path)
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		return e
	}
	r.mu.Lock()
	r.front = cmd
	r.frontDone = make(chan struct{})
	r.mu.Unlock()
	go func() { _ = cmd.Wait(); log.Close(); close(r.frontDone) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		host, port, _ := net.SplitHostPort(r.Input.Config.Listener.Addr)
		if host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		if host == "::" {
			host = "::1"
		}
		if checkAuth(r.ctx, net.JoinHostPort(host, port), r.Input.Config.Listener.Users[0]) == nil {
			return nil
		}
		select {
		case <-r.frontDone:
			return errors.New("入口 GOST 提前退出")
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
	return errors.New("入口 GOST 启动超时")
}
func (r *Runtime) Run(ctx context.Context) error {
	defer r.Stop()
	if e := r.Start(ctx); e != nil {
		return e
	}
	timer := time.NewTicker(500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.frontDone:
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("入口 GOST 意外退出")
		case <-timer.C:
			r.mu.Lock()
			g := r.current
			due := []string{}
			if g != nil {
				for _, p := range g.cfg.Profiles {
					if !time.Now().Before(g.next[p.Name]) {
						due = append(due, p.Name)
					}
				}
			}
			r.mu.Unlock()
			for _, p := range due {
				if e := r.probeGeneration(g, p, false); e != nil {
					return e
				}
			}
			r.mutation.Lock()
			r.mu.Lock()
			retained := map[*Worker]bool{}
			gens := []*Generation{}
			for _, g := range r.generations {
				if g == r.current || g == r.prepared || g.pending > 0 {
					gens = append(gens, g)
					for _, w := range g.workers {
						retained[w] = true
					}
				}
			}
			r.generations = gens
			dead := []*Worker{}
			live := []*Worker{}
			for k, w := range r.workers {
				if !retained[w] && w.refs.Load() == 0 {
					dead = append(dead, w)
					delete(r.workers, k)
				} else {
					live = append(live, w)
				}
			}
			r.mu.Unlock()
			for _, w := range dead {
				w.stop()
			}
			for _, w := range live {
				_ = w.start()
			}
			r.mutation.Unlock()
		}
	}
}
func (r *Runtime) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	for _, l := range r.listeners {
		l.Close()
	}
	r.wg.Wait()
	r.mutation.Lock()
	defer r.mutation.Unlock()
	r.mu.Lock()
	workers := []*Worker{}
	for _, w := range r.workers {
		workers = append(workers, w)
	}
	r.mu.Unlock()
	for _, w := range workers {
		w.stop()
	}
	if r.frontDone != nil {
		<-r.frontDone
	}
	_ = os.Remove(filepath.Join(r.Dir, "control.sock"))
}

type Request struct {
	Command  string        `json:"command"`
	Config   config.Config `json:"config,omitempty"`
	Revision string        `json:"revision,omitempty"`
	Profile  string        `json:"profile,omitempty"`
}

func (r *Runtime) control(c net.Conn) {
	defer c.Close()
	stop := context.AfterFunc(r.ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(120 * time.Second))
	line, e := bufio.NewReader(io.LimitReader(c, 1048578)).ReadBytes('\n')
	var req Request
	var result any
	var op error
	if e != nil || len(line) > 1048576 || json.Unmarshal(line, &req) != nil {
		op = errors.New("控制请求无效")
	} else {
		switch req.Command {
		case "status":
			result = r.Status()
		case "prepare":
			op = r.Prepare(req.Config, req.Revision)
			result = map[string]string{"preparedRevision": req.Revision}
		case "commit":
			result, op = r.Commit(req.Revision)
		case "abort":
			r.Abort()
			result = r.Status()
		case "probe":
			result, op = r.Probe(req.Profile)
		default:
			op = errors.New("控制命令无效")
		}
	}
	if op != nil {
		_, _ = c.Write(config.Bytes(map[string]any{"error": "运行时请求失败"}))
	} else {
		_, _ = c.Write(config.Bytes(map[string]any{"data": result}))
	}
}
func RPC(ctx context.Context, socket string, req Request, out any) error {
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if e != nil {
		return errors.New("无法联系转发运行时")
	}
	defer c.Close()
	d := time.Now().Add(120 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(d) {
		d = deadline
	}
	c.SetDeadline(d)
	if _, e = c.Write(append(mustJSON(req), '\n')); e != nil {
		return e
	}
	var reply struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if e = json.NewDecoder(io.LimitReader(c, 1048577)).Decode(&reply); e != nil {
		return errors.New("运行时响应无效")
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if out != nil {
		return json.Unmarshal(reply.Data, out)
	}
	return nil
}
func mustJSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(fmt.Sprint(e))
	}
	return b
}
