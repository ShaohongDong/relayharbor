package manage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const LEGOVersion = "5.5.2"
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"
const tlsRenewUnit = "gost-deploy-tls-renew.service"
const tlsReloadUnit = "gost-deploy-tls-reload.service"
const tlsTimerUnit = "gost-deploy-tls-renew.timer"

var LEGOPinned = map[string]string{
	"amd64": "2a35505089e7772c92e1e9ac144df91151ef2eca8568630db0ff91fca06d9bef",
	"arm64": "15b14ec2ab14fde69cc8396eb0204c5ce4327e31a486953225a6059b26db3e8c",
}
var ErrTLSBusy = errors.New("HTTPS 操作正在进行，请稍后查看状态")

type TLSOptions struct {
	Domain, Email      string
	AcceptTOS          bool
	Archive, Checksums string
	// Server is injectable for isolated ACME acceptance tests; the CLI uses Let's Encrypt.
	Server string
}

type TLSConfig struct {
	Schema       int               `json:"schema"`
	Domain       string            `json:"domain"`
	Email        string            `json:"email"`
	Server       string            `json:"server"`
	Enabled      bool              `json:"enabled"`
	Provisioning bool              `json:"provisioning,omitempty"`
	Client       Release           `json:"client"`
	Resources    map[string]string `json:"resources"`
}

type TLSResult struct {
	Operation  string    `json:"operation"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Acquired   bool      `json:"acquired"`
	Deployed   bool      `json:"deployed"`
	Error      string    `json:"error,omitempty"`
}

type TLSStatus struct {
	Configured   bool       `json:"configured"`
	Enabled      bool       `json:"enabled"`
	Domain       string     `json:"domain,omitempty"`
	URL          string     `json:"url,omitempty"`
	Issuer       string     `json:"issuer,omitempty"`
	NotBefore    *time.Time `json:"notBefore,omitempty"`
	NotAfter     *time.Time `json:"notAfter,omitempty"`
	Expiring     bool       `json:"expiring"`
	NginxActive  bool       `json:"nginxActive"`
	TimerEnabled bool       `json:"timerEnabled"`
	TimerActive  bool       `json:"timerActive"`
	NextCheck    string     `json:"nextCheck,omitempty"`
	Operation    string     `json:"operation,omitempty"`
	Running      bool       `json:"running"`
	LastResult   *TLSResult `json:"lastResult,omitempty"`
	Warning      string     `json:"warning,omitempty"`
}

type tlsPaths struct{ config, root, nginx, site, webroot, units, lock string }

func (m *Manager) tlsPaths() tlsPaths {
	etc := filepath.Dir(filepath.Dir(m.P.Manifest))
	return tlsPaths{
		filepath.Join(filepath.Dir(m.P.Manifest), "tls.json"),
		filepath.Join(filepath.Dir(m.P.State), "tls"),
		filepath.Join(etc, "nginx"),
		filepath.Join(etc, "nginx/conf.d/gost-deploy.conf"),
		filepath.Join(etc, "gost-deploy/acme-webroot"),
		filepath.Dir(m.P.Unit), m.P.Lock + ".tls",
	}
}

func ValidateTLSOptions(o TLSOptions) error {
	if !o.AcceptTOS {
		return errors.New("签发证书需要 --accept-tos，同意 Let's Encrypt 服务条款")
	}
	if o.Domain != strings.ToLower(o.Domain) || len(o.Domain) > 253 || !strings.Contains(o.Domain, ".") || net.ParseIP(o.Domain) != nil {
		return errors.New("需要小写 ASCII 完整域名；不支持 IP、通配符或 URL")
	}
	label := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	for _, part := range strings.Split(o.Domain, ".") {
		if !label.MatchString(part) {
			return errors.New("域名格式无效")
		}
	}
	a, e := mail.ParseAddress(o.Email)
	if e != nil || a.Address != o.Email || strings.ContainsAny(o.Email, "\r\n") {
		return errors.New("需要有效的注册邮箱")
	}
	if (o.Archive == "") != (o.Checksums == "") {
		return errors.New("离线 lego 需要同时提供 --lego-archive 和 --lego-checksums")
	}
	return nil
}

func (m *Manager) tlsLocked(fn func() error) error {
	f, e := os.OpenFile(m.tlsPaths().lock, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return ErrTLSBusy
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (m *Manager) loadTLS() (*TLSConfig, error) {
	b, e := os.ReadFile(m.tlsPaths().config)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var c TLSConfig
	if e = json.Unmarshal(b, &c); e != nil || c.Schema != 1 {
		return nil, errors.New("HTTPS 状态无效")
	}
	if e = ValidateTLSOptions(TLSOptions{Domain: c.Domain, Email: c.Email, AcceptTOS: true}); e != nil {
		return nil, e
	}
	return &c, nil
}

func hashText(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (m *Manager) verifyTLS(c *TLSConfig) error {
	p := m.tlsPaths()
	allowed := map[string]bool{p.site: true}
	for _, u := range []string{tlsRenewUnit, tlsReloadUnit, tlsTimerUnit} {
		allowed[filepath.Join(p.units, u)] = true
	}
	if !c.Enabled && c.Resources[p.site] == "" {
		delete(allowed, p.site)
	}
	if len(c.Resources) != len(allowed) {
		return errors.New("HTTPS 资源记录不完整")
	}
	for path, want := range c.Resources {
		if !allowed[path] {
			return errors.New("HTTPS 状态包含非受管路径")
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil || real != path {
			return errors.New("HTTPS 配置路径包含外部链接")
		}
		got, e := Digest(path)
		if e != nil || got != want {
			return errors.New("HTTPS 配置被外部修改，拒绝覆盖")
		}
	}
	return m.verifyLEGO(c.Client)
}

func (m *Manager) verifyLEGO(r Release) error {
	root := filepath.Join(filepath.Dir(m.P.Releases), "acme")
	real, e := filepath.EvalSymlinks(r.Binary)
	if e != nil || real != r.Binary || !within(real, root) {
		return errors.New("ACME 客户端不属于受管目录")
	}
	got, e := Digest(real)
	if e != nil || got != r.SHA256 {
		return errors.New("ACME 客户端校验失败")
	}
	return nil
}

func (m *Manager) prepareLEGO(ctx context.Context, archive, sums string) (Release, error) {
	arch := runtime.GOARCH
	want, ok := LEGOPinned[arch]
	if !ok {
		return Release{}, errors.New("不支持的 ACME 客户端架构")
	}
	root := filepath.Join(filepath.Dir(m.P.Releases), "acme")
	if e := os.MkdirAll(root, 0700); e != nil {
		return Release{}, e
	}
	tmp, e := os.MkdirTemp(root, ".download-")
	if e != nil {
		return Release{}, e
	}
	defer os.RemoveAll(tmp)
	name := "lego_v" + LEGOVersion + "_linux_" + arch + ".tar.gz"
	if archive == "" {
		archive, sums = filepath.Join(tmp, name), filepath.Join(tmp, "checksums.txt")
		base := "https://github.com/go-acme/lego/releases/download/v" + LEGOVersion + "/"
		if e = Download(ctx, base+name, archive); e != nil {
			return Release{}, e
		}
		if e = Download(ctx, base+"lego_"+LEGOVersion+"_checksums.txt", sums); e != nil {
			return Release{}, e
		}
	}
	hash, e := Digest(archive)
	if e != nil || hash != want {
		return Release{}, errors.New("lego 发布包校验失败")
	}
	listed, e := Checksum(sums, name)
	if e != nil || listed != want {
		return Release{}, errors.New("lego 校验清单不符")
	}
	f, e := os.Open(archive)
	if e != nil {
		return Release{}, e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return Release{}, e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	binary := filepath.Join(tmp, "lego")
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return Release{}, e
		}
		if h.Name != "lego" {
			continue
		}
		if found || h.Typeflag != tar.TypeReg || h.Size > 256<<20 {
			return Release{}, errors.New("lego 发布包内容无效")
		}
		found = true
		out, e := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if e != nil {
			return Release{}, e
		}
		_, ce := io.Copy(out, tr)
		closeErr := out.Close()
		if ce != nil {
			return Release{}, ce
		}
		if closeErr != nil {
			return Release{}, closeErr
		}
	}
	if !found {
		return Release{}, errors.New("发布包缺少 lego")
	}
	hash, e = Digest(binary)
	if e != nil {
		return Release{}, e
	}
	dir := filepath.Join(root, LEGOVersion, arch, hash)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return Release{}, e
	}
	r := Release{LEGOVersion, arch, filepath.Join(dir, "lego"), hash}
	if !exists(r.Binary) {
		if e = os.Rename(binary, r.Binary); e != nil {
			return Release{}, e
		}
	}
	if e = m.verifyLEGO(r); e != nil {
		return Release{}, e
	}
	return r, nil
}

func tlsUnit(op string) string {
	return fmt.Sprintf(`[Unit]
Description=GOST HTTPS %s
After=network-online.target nginx.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/gostctl _tls %s
TimeoutStartSec=330
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
`, op, op)
}

const tlsTimer = `[Unit]
Description=Check GOST HTTPS certificate renewal twice daily

[Timer]
OnCalendar=*-*-* 00,12:00:00
RandomizedDelaySec=1h
Persistent=true
Unit=gost-deploy-tls-renew.service

[Install]
WantedBy=timers.target
`

func (m *Manager) tlsSite(c *TLSConfig, https bool, challengeDomain string) string {
	p := m.tlsPaths()
	domains := c.Domain
	if challengeDomain != "" && challengeDomain != c.Domain {
		domains += " " + challengeDomain
	}
	http := fmt.Sprintf(`# Managed by gost-deploy. Use gostctl tls commands.
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name %s;
    location ^~ /.well-known/acme-challenge/ {
        root %s;
        default_type text/plain;
        try_files $uri =404;
    }
    location / { %s }
    location ^~ /sub/ { access_log off; error_log /dev/null; return 404; }
}
`, domains, p.webroot, "return 404;")
	if !https {
		return http
	}
	http = strings.Replace(http, "return 404;", "return 308 https://"+c.Domain+"$request_uri;", 1)
	return http + fmt.Sprintf(`server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    server_name %s;
    if ($host != %s) { return 444; }
    ssl_certificate %s/live/fullchain.pem;
    ssl_certificate_key %s/live/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    client_max_body_size 1m;
    location ^~ /sub/ {
        access_log off;
        error_log /dev/null;
        proxy_pass http://127.0.0.1:18080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Connection "";
        proxy_cache off;
        proxy_read_timeout 15s;
    }
    location / {
        proxy_pass http://127.0.0.1:18080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Connection "";
        proxy_set_header Authorization $http_authorization;
        proxy_set_header If-Match $http_if_match;
        proxy_cache off;
        proxy_read_timeout 190s;
        proxy_send_timeout 190s;
    }
}
`, c.Domain, c.Domain, p.root, p.root)
}

func (m *Manager) nginxReload(ctx context.Context) error {
	if _, e := m.Exec(ctx, "nginx", "-t"); e != nil {
		return errors.New("Nginx 配置校验失败；请查看 systemctl status nginx")
	}
	if _, e := m.sys(ctx, "reload", "nginx.service"); e != nil {
		return errors.New("Nginx 重载失败")
	}
	if m.TLSProbe != nil {
		return m.TLSProbe(ctx)
	}
	// systemctl reload acknowledges the signal, not completion. Wait for the requested listener/cert.
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if e := m.nginxProbe(probeCtx); e == nil {
			return nil
		}
		select {
		case <-probeCtx.Done():
			return errors.New("Nginx 重载后未确认预期站点或证书")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *Manager) nginxProbe(ctx context.Context) error {
	p := m.tlsPaths()
	site, e := os.ReadFile(p.site)
	if os.IsNotExist(e) {
		for _, addr := range []string{"127.0.0.1:80", "127.0.0.1:443"} {
			c, e := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", addr)
			if e == nil {
				c.Close()
				return errors.New("旧监听尚未关闭")
			}
		}
		return nil
	}
	if e != nil {
		return e
	}
	if strings.Contains(string(site), "ssl_certificate ") {
		cert, e := os.ReadFile(filepath.Join(p.root, "live/fullchain.pem"))
		if e != nil {
			return e
		}
		block, _ := pem.Decode(cert)
		if block == nil {
			return errors.New("缺少已部署证书")
		}
		c, e := m.loadTLS()
		if e != nil || c == nil {
			return errors.New("缺少 HTTPS 状态")
		}
		// Pin the exact locally verified certificate, including during certificate rollback.
		cfg := &tls.Config{ServerName: c.Domain, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, block.Bytes) {
				return errors.New("Nginx 尚未启用候选证书")
			}
			return nil
		}}
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 500 * time.Millisecond}, Config: cfg}
		conn, e := dialer.DialContext(ctx, "tcp", "127.0.0.1:443")
		if e == nil {
			conn.Close()
		}
		return e
	}
	path := filepath.Join(p.webroot, ".well-known/acme-challenge/.gost-deploy-health")
	if e = Atomic(path, []byte("gost-deploy-health"), 0644, -1); e != nil {
		return e
	}
	defer os.Remove(path)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1/.well-known/acme-challenge/.gost-deploy-health", nil)
	if e != nil {
		return e
	}
	response, e := client.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 128))
	if e != nil || response.StatusCode != 200 || string(body) != "gost-deploy-health" {
		return errors.New("Nginx 验证路径不可用")
	}
	return nil
}

func (m *Manager) writeTLSSite(c *TLSConfig, text string) error {
	p := m.tlsPaths()
	if e := Atomic(p.site, []byte(text), 0644, -1); e != nil {
		return e
	}
	if c.Resources == nil {
		c.Resources = map[string]string{}
	}
	c.Resources[p.site] = hashText([]byte(text))
	return writeJSON(p.config, c, 0600, -1)
}

// TLSSetup manages only a dedicated host, and never takes over an existing nginx installation.
func (m *Manager) TLSSetup(ctx context.Context, o TLSOptions) error {
	if e := ValidateTLSOptions(o); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return m.tlsLocked(func() error {
		if e := m.guardAnyTLSTLS(o.Domain); e != nil {
			return e
		}
		s, e := m.Load(false)
		if e != nil {
			return e
		}
		if e = m.Managed(ctx, s); e != nil {
			return e
		}
		if busy, _ := m.tlsOperation(ctx); busy != "" {
			return ErrTLSBusy
		}
		c, e := m.loadTLS()
		if e != nil {
			return e
		}
		p := m.tlsPaths()
		if c == nil {
			if exists(p.nginx) {
				return errors.New("发现已有 Nginx 配置；仅支持专用新服务器")
			}
			if _, e := exec.LookPath("nginx"); e == nil {
				return errors.New("发现已有 Nginx 程序，拒绝接管")
			}
			if _, e = m.Exec(ctx, "dpkg-query", "-W", "-f=${Status}", "nginx-common"); e == nil {
				return errors.New("发现已有 Nginx 安装，拒绝接管")
			}
			b, re := m.sys(ctx, "show", "nginx.service", "-p", "LoadState", "--value")
			if re != nil || strings.TrimSpace(string(b)) != "not-found" {
				return errors.New("发现已有 Nginx unit，拒绝接管")
			}
			for _, port := range []string{":80", ":443"} {
				l, e := net.Listen("tcp", port)
				if e != nil {
					return errors.New("80 或 443 端口已占用，拒绝接管")
				}
				l.Close()
			}
			for _, u := range []string{tlsRenewUnit, tlsReloadUnit, tlsTimerUnit} {
				if exists(filepath.Join(p.units, u)) {
					return errors.New("发现外部 HTTPS systemd unit")
				}
			}
			// Download and verify the client before installing any system package.
			r, e := m.prepareLEGO(ctx, o.Archive, o.Checksums)
			if e != nil {
				return e
			}
			c = &TLSConfig{Schema: 1, Domain: o.Domain, Email: o.Email, Server: LetsEncrypt, Client: r, Resources: map[string]string{}, Provisioning: true}
			if o.Server != "" {
				c.Server = o.Server
			}
			// Record ownership before apt so an interrupted installation can be resumed safely.
			if e = writeJSON(p.config, c, 0600, -1); e != nil {
				return e
			}
		}
		if c.Provisioning {
			if e = m.verifyLEGO(c.Client); e != nil {
				return e
			}
			for _, dir := range []string{filepath.Join(p.nginx, "conf.d"), filepath.Join(p.nginx, "sites-enabled")} {
				entries, re := os.ReadDir(dir)
				if re != nil && !os.IsNotExist(re) {
					return re
				}
				for _, entry := range entries {
					path := filepath.Join(dir, entry.Name())
					if path == filepath.Join(p.nginx, "sites-enabled/default") {
						continue
					}
					if path == p.site {
						b, re := os.ReadFile(path)
						if re == nil && string(b) == m.tlsSite(c, false, "") {
							continue
						}
					}
					return errors.New("HTTPS 安装未完成且发现外部 Nginx 站点，拒绝覆盖")
				}
			}
			if _, e = m.Exec(ctx, "apt-get", "update"); e != nil {
				return e
			}
			if _, e = m.Exec(ctx, "apt-get", "install", "-y", "--no-install-recommends", "nginx"); e != nil {
				return e
			}
			// This link comes only from the package installed above; never remove a foreign default site.
			defaultSite := filepath.Join(p.nginx, "sites-enabled/default")
			if exists(defaultSite) {
				target, re := os.Readlink(defaultSite)
				if re != nil || target != "/etc/nginx/sites-available/default" {
					return errors.New("发现外部 Nginx 默认站点，拒绝覆盖")
				}
				if e = os.Remove(defaultSite); e != nil {
					return e
				}
			}
			for _, item := range []struct{ name, text string }{{tlsRenewUnit, tlsUnit("renew")}, {tlsReloadUnit, tlsUnit("reload")}, {tlsTimerUnit, tlsTimer}} {
				path := filepath.Join(p.units, item.name)
				if exists(path) {
					b, re := os.ReadFile(path)
					if re != nil || string(b) != item.text {
						return errors.New("HTTPS 安装未完成且发现外部 unit")
					}
				}
				if e = Atomic(path, []byte(item.text), 0644, -1); e != nil {
					return e
				}
				c.Resources[path] = hashText([]byte(item.text))
			}
			if e = m.tlsWebroot(); e != nil {
				return e
			}
			if e = os.MkdirAll(p.root, 0700); e != nil {
				return e
			}
			if e = m.writeTLSSite(c, m.tlsSite(c, false, "")); e != nil {
				return e
			}
			if _, e = m.sys(ctx, "daemon-reload"); e != nil {
				return e
			}
			if _, e = m.Exec(ctx, "nginx", "-t"); e != nil {
				return e
			}
			if _, e = m.sys(ctx, "enable", "--now", "nginx.service"); e != nil {
				return e
			}
			if e = m.nginxReload(ctx); e != nil {
				return e
			}
			c.Provisioning = false
			if e = writeJSON(p.config, c, 0600, -1); e != nil {
				return e
			}
		} else if e = m.verifyTLS(c); e != nil {
			return e
		}
		if c.Resources[p.site] == "" {
			if e = m.writeTLSSite(c, m.tlsSite(c, false, "")); e != nil {
				return e
			}
		}
		if e = m.verifyTLSUnits(ctx); e != nil {
			return e
		}
		if _, e = m.sys(ctx, "enable", "--now", "nginx.service"); e != nil {
			return e
		}
		if e = m.nginxReload(ctx); e != nil {
			return e
		}
		return m.setupTLSCertificate(ctx, c, o)
	})
}

func (m *Manager) tlsWebroot() error {
	root := m.tlsPaths().webroot
	for _, path := range []string{root, filepath.Join(root, ".well-known"), filepath.Join(root, ".well-known/acme-challenge")} {
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			if e = os.Mkdir(path, 0755); e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("HTTP 验证目录包含外部链接或文件")
		}
		// These directories contain only public HTTP-01 tokens. The parent's umask must
		// not hide them from nginx; account and certificate stores remain root-only.
		if e = os.Chmod(path, 0755); e != nil {
			return e
		}
	}
	return nil
}

func (m *Manager) setupTLSCertificate(ctx context.Context, c *TLSConfig, o TLSOptions) (err error) {
	p := m.tlsPaths()
	old := *c
	old.Resources = map[string]string{}
	for k, v := range c.Resources {
		old.Resources[k] = v
	}
	oldSite, e := os.ReadFile(p.site)
	if e != nil {
		return e
	}
	oldLive, e := capture(filepath.Join(p.root, "live"))
	if e != nil {
		return e
	}
	defer func() {
		if err != nil {
			if e := m.restore([]fileSnapshot{oldLive}); e != nil {
				err = errors.Join(err, e)
			}
			if e := Atomic(p.site, oldSite, 0644, -1); e != nil {
				err = errors.Join(err, e)
			}
			if e := writeJSON(p.config, &old, 0600, -1); e != nil {
				err = errors.Join(err, e)
			}
			recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if !old.Enabled {
				if _, e := m.sys(recovery, "disable", "--now", tlsTimerUnit); e != nil {
					err = errors.Join(err, e)
				}
			}
			if e := m.nginxReload(recovery); e != nil {
				err = errors.Join(err, e)
			}
		}
	}()
	if o.Domain != c.Domain {
		if e = m.writeTLSSite(c, m.tlsSite(c, c.Enabled, o.Domain)); e != nil {
			return e
		}
		if e = m.nginxReload(ctx); e != nil {
			return e
		}
	}
	c.Domain, c.Email = o.Domain, o.Email
	result := TLSResult{Operation: "setup", StartedAt: time.Now().UTC()}
	defer func() {
		result.FinishedAt = time.Now().UTC()
		if err != nil {
			result.Error = err.Error()
		}
		_ = writeJSON(filepath.Join(p.root, "result.json"), result, 0600, -1)
	}()
	beforeCert, _ := os.ReadFile(m.candidateTLS(c) + ".crt")
	if e = m.obtainTLS(ctx, c); e != nil {
		return e
	}
	afterCert, _ := os.ReadFile(m.candidateTLS(c) + ".crt")
	result.Acquired = string(beforeCert) != string(afterCert)
	c.Enabled = true
	if e = m.writeTLSSite(c, m.tlsSite(c, true, "")); e != nil {
		return e
	}
	if e = m.deployTLS(ctx, c); e != nil {
		return e
	}
	if e = m.nginxReload(ctx); e != nil {
		return e
	}
	if _, e = m.sys(ctx, "enable", "--now", tlsTimerUnit); e != nil {
		return e
	}
	result.Deployed = true
	return nil
}

func (m *Manager) obtainTLS(ctx context.Context, c *TLSConfig) error {
	if e := m.verifyLEGO(c.Client); e != nil {
		return e
	}
	p := m.tlsPaths()
	store := filepath.Join(p.root, "acme", hashText([]byte(c.Server)), c.Domain)
	if e := os.MkdirAll(store, 0700); e != nil {
		return e
	}
	// The systemd timer already randomizes checks; suppress lego's additional potentially long sleep.
	args := []string{"run", "--accept-tos", "--email", c.Email, "--domains", c.Domain, "--server", c.Server, "--path", store, "--http", "--http.webroot", p.webroot, "--http-timeout", "30", "--no-random-sleep"}
	// Run in a dedicated child so its public challenge files remain readable even under
	// the API/systemd 0077 umask, without changing the concurrent manager's process umask.
	b, e := m.Exec(ctx, m.P.Control, append([]string{"_acme"}, args...)...)
	if len(b) > 0 {
		log.Printf("ACME: %s", strings.ReplaceAll(string(b[:min(len(b), 8192)]), c.Email, "[email]"))
	}
	if e != nil {
		return errors.New("证书获取失败；请检查 DNS、80 端口、CA 限制与 systemd journal")
	}
	return nil
}

func (m *Manager) TLSExec(args []string) error {
	c, e := m.loadTLS()
	if e != nil {
		return e
	}
	if c == nil {
		return errors.New("HTTPS 尚未配置")
	}
	if e = m.verifyLEGO(c.Client); e != nil {
		return e
	}
	syscall.Umask(0022)
	return syscall.Exec(c.Client.Binary, append([]string{c.Client.Binary}, args...), os.Environ())
}

func (m *Manager) candidateTLS(c *TLSConfig) string {
	return filepath.Join(m.tlsPaths().root, "acme", hashText([]byte(c.Server)), c.Domain, "certificates", c.Domain)
}

func certificate(cert, key []byte, domain string) (*x509.Certificate, error) {
	pair, e := tls.X509KeyPair(cert, key)
	if e != nil {
		return nil, errors.New("证书与私钥不匹配")
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return nil, e
	}
	if e = leaf.VerifyHostname(domain); e != nil {
		return nil, errors.New("证书域名不匹配")
	}
	if time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return nil, errors.New("证书不在有效期内")
	}
	return leaf, nil
}

// Publish a complete cert/key generation, then atomically switch the directory link.
func (m *Manager) deployTLS(ctx context.Context, c *TLSConfig) (err error) {
	p := m.tlsPaths()
	candidate := m.candidateTLS(c)
	cert, e := os.ReadFile(candidate + ".crt")
	if e != nil {
		return errors.New("缺少候选证书")
	}
	key, e := os.ReadFile(candidate + ".key")
	if e != nil {
		return errors.New("缺少候选私钥")
	}
	if _, e = certificate(cert, key, c.Domain); e != nil {
		return e
	}
	live := filepath.Join(p.root, "live")
	old, _ := os.Readlink(live)
	gen := filepath.Join(p.root, "certificates", hashText(append(append([]byte{}, cert...), key...)))
	if e = os.MkdirAll(gen, 0700); e != nil {
		return e
	}
	if e = Atomic(filepath.Join(gen, "fullchain.pem"), cert, 0600, -1); e != nil {
		return e
	}
	if e = Atomic(filepath.Join(gen, "privkey.pem"), key, 0600, -1); e != nil {
		return e
	}
	if old == gen {
		return nil
	}
	if e = symlink(live, gen); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			if old != "" {
				err = errors.Join(err, symlink(live, old))
			} else {
				err = errors.Join(err, os.Remove(live))
			}
			recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err = errors.Join(err, m.nginxReload(recovery))
		}
	}()
	// During initial setup the HTTP-only configuration does not yet reference live.
	if e = m.nginxReload(ctx); e != nil {
		return e
	}
	return m.deployAnyTLSCert(ctx)
}

func (m *Manager) tlsOperation(ctx context.Context) (string, error) {
	for _, u := range []struct{ name, op string }{{tlsRenewUnit, "renew"}, {tlsReloadUnit, "reload"}} {
		b, e := m.sys(ctx, "show", u.name, "-p", "ActiveState", "--value")
		if e != nil {
			return "", e
		}
		s := strings.TrimSpace(string(b))
		if s == "activating" || s == "active" || s == "deactivating" {
			return u.op, nil
		}
	}
	return "", nil
}

func (m *Manager) TLSStatus(ctx context.Context) (TLSStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s := TLSStatus{}
	c, e := m.loadTLS()
	if e != nil {
		return s, e
	}
	if c == nil {
		return s, nil
	}
	s.Configured, s.Enabled, s.Domain, s.URL = true, c.Enabled, c.Domain, "https://"+c.Domain+"/"
	if e = m.verifyTLS(c); e != nil {
		s.Warning = e.Error()
	}
	if e = m.verifyTLSUnits(ctx); e != nil {
		s.Warning = e.Error()
	}
	if c.Provisioning {
		s.Warning = "HTTPS 安装尚未完成，请重新执行原 tls setup 命令继续部署"
	}
	_, e = m.sys(ctx, "is-active", "--quiet", "nginx.service")
	s.NginxActive = e == nil
	_, e = m.sys(ctx, "is-enabled", "--quiet", tlsTimerUnit)
	s.TimerEnabled = e == nil
	_, e = m.sys(ctx, "is-active", "--quiet", tlsTimerUnit)
	s.TimerActive = e == nil
	b, _ := m.sys(ctx, "show", tlsTimerUnit, "-p", "NextElapseUSecRealtime", "--value")
	s.NextCheck = strings.TrimSpace(string(b))
	s.Operation, e = m.tlsOperation(ctx)
	if e != nil {
		s.Warning = "无法查询 HTTPS 任务状态"
	}
	s.Running = s.Operation != ""
	if s.Enabled {
		cert, ce := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/fullchain.pem"))
		key, ke := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/privkey.pem"))
		if ce == nil && ke == nil {
			pair, err := tls.X509KeyPair(cert, key)
			if err == nil {
				leaf, err := x509.ParseCertificate(pair.Certificate[0])
				if err == nil {
					s.Issuer = leaf.Issuer.String()
					s.NotBefore = &leaf.NotBefore
					s.NotAfter = &leaf.NotAfter
					s.Expiring = time.Until(leaf.NotAfter) < 14*24*time.Hour
				}
			}
		}
		if s.NotAfter == nil {
			s.Warning = "无法读取已部署证书"
		}
	}
	b, e = os.ReadFile(filepath.Join(m.tlsPaths().root, "result.json"))
	if e == nil {
		var r TLSResult
		if json.Unmarshal(b, &r) == nil {
			s.LastResult = &r
		}
	}
	return s, nil
}

func (m *Manager) TLSStart(ctx context.Context, op string) error {
	if op != "renew" && op != "reload" {
		return errors.New("不支持的 HTTPS 操作")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return m.tlsLocked(func() error {
		c, e := m.loadTLS()
		if e != nil {
			return e
		}
		if c == nil || !c.Enabled {
			return errors.New("HTTPS 尚未启用，请先执行 gostctl tls setup")
		}
		if e = m.verifyTLS(c); e != nil {
			return e
		}
		if e = m.verifyTLSUnits(ctx); e != nil {
			return e
		}
		busy, e := m.tlsOperation(ctx)
		if e != nil {
			return e
		}
		if busy != "" {
			return ErrTLSBusy
		}
		unit := tlsRenewUnit
		if op == "reload" {
			unit = tlsReloadUnit
		}
		_, e = m.sys(ctx, "start", "--no-block", unit)
		return e
	})
}

// TLSRun is called by systemd, independently of the API lifetime and proxy config lock.
func (m *Manager) TLSRun(ctx context.Context, op string) error {
	if op != "renew" && op != "reload" {
		return errors.New("不支持的 HTTPS 操作")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	work := func() (err error) {
		c, e := m.loadTLS()
		if e != nil {
			return e
		}
		if c == nil || !c.Enabled {
			return errors.New("HTTPS 尚未启用")
		}
		r := TLSResult{Operation: op, StartedAt: time.Now().UTC()}
		defer func() {
			r.FinishedAt = time.Now().UTC()
			if err != nil {
				r.Error = err.Error()
			}
			err = errors.Join(err, writeJSON(filepath.Join(m.tlsPaths().root, "result.json"), r, 0600, -1))
		}()
		if e = m.verifyTLS(c); e != nil {
			return e
		}
		if e = m.verifyTLSUnits(ctx); e != nil {
			return e
		}
		if op == "renew" {
			// First finish a previously acquired but undeployed certificate, even when ACME is unavailable.
			candidate := m.candidateTLS(c)
			want, we := os.ReadFile(candidate + ".crt")
			live, le := os.ReadFile(filepath.Join(m.tlsPaths().root, "live/fullchain.pem"))
			if we == nil && (le != nil || string(want) != string(live)) {
				key, ke := os.ReadFile(candidate + ".key")
				if _, ce := certificate(want, key, c.Domain); ke == nil && ce == nil {
					if e = m.deployTLS(ctx, c); e != nil {
						return e
					}
					r.Deployed = true
				}
			}
			if e = m.obtainTLS(ctx, c); e != nil {
				return e
			}
			after, _ := os.ReadFile(candidate + ".crt")
			r.Acquired = string(after) != string(want)
			live, le = os.ReadFile(filepath.Join(m.tlsPaths().root, "live/fullchain.pem"))
			if le != nil || string(after) != string(live) {
				if e = m.deployTLS(ctx, c); e != nil {
					return e
				}
				r.Deployed = true
			}
		}
		return m.nginxReload(ctx)
	}
	// The worker can start before systemctl --no-block returns and releases the submit lock.
	for attempt := 0; ; attempt++ {
		err := m.tlsLocked(work)
		if !errors.Is(err, ErrTLSBusy) || attempt >= 20 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *Manager) TLSDisable(ctx context.Context, purge bool) error {
	return m.tlsLocked(func() error { return m.disableTLS(ctx, purge) })
}

func (m *Manager) verifyTLSUnits(ctx context.Context) error {
	for _, u := range []string{tlsRenewUnit, tlsReloadUnit, tlsTimerUnit} {
		b, e := m.sys(ctx, "show", u, "-p", "FragmentPath", "--value")
		if e != nil || strings.TrimSpace(string(b)) != filepath.Join(m.tlsPaths().units, u) {
			return errors.New("HTTPS 实际 unit 路径不符")
		}
		b, e = m.sys(ctx, "show", u, "-p", "DropInPaths", "--value")
		if e != nil || strings.TrimSpace(string(b)) != "" {
			return errors.New("发现外部 HTTPS systemd drop-in")
		}
	}
	return nil
}

// Manager replacement and uninstall share this lock with all certificate work.
func (m *Manager) tlsMaintenance(ctx context.Context, fn func() error) error {
	c, e := m.loadTLS()
	if e != nil {
		return e
	}
	if c == nil {
		return fn()
	}
	return m.tlsLocked(func() error {
		busy, e := m.tlsOperation(ctx)
		if e != nil {
			return e
		}
		if busy != "" {
			return ErrTLSBusy
		}
		return fn()
	})
}

func (m *Manager) disableTLS(ctx context.Context, purge bool) error {
	if e := m.guardAnyTLSTLS(""); e != nil {
		return e
	}
	c, e := m.loadTLS()
	if e != nil {
		return e
	}
	if c == nil {
		return nil
	}
	if c.Provisioning {
		return m.disableTLSProvisioning(ctx, c, purge)
	}
	if busy, e := m.tlsOperation(ctx); e != nil {
		return e
	} else if busy != "" {
		return ErrTLSBusy
	}
	if e = m.verifyTLS(c); e != nil {
		return e
	}
	if e = m.verifyTLSUnits(ctx); e != nil {
		return e
	}
	_, wasEnabledErr := m.sys(ctx, "is-enabled", "--quiet", tlsTimerUnit)
	_, wasActiveErr := m.sys(ctx, "is-active", "--quiet", tlsTimerUnit)
	oldConfig, e := capture(m.tlsPaths().config)
	if e != nil {
		return e
	}
	oldSite, e := capture(m.tlsPaths().site)
	if e != nil {
		return e
	}
	recoverSite := func(cause error) error {
		recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cause = errors.Join(cause, m.restore([]fileSnapshot{oldConfig, oldSite}), m.nginxReload(recovery))
		if wasEnabledErr == nil {
			_, e := m.sys(recovery, "enable", tlsTimerUnit)
			cause = errors.Join(cause, e)
		}
		if wasActiveErr == nil {
			_, e := m.sys(recovery, "start", tlsTimerUnit)
			cause = errors.Join(cause, e)
		}
		return cause
	}
	if _, e = m.sys(ctx, "disable", "--now", tlsTimerUnit); e != nil {
		return e
	}
	if e = os.Remove(m.tlsPaths().site); e != nil && !os.IsNotExist(e) {
		return recoverSite(e)
	}
	if e = m.nginxReload(ctx); e != nil {
		return recoverSite(e)
	}
	c.Enabled = false
	delete(c.Resources, m.tlsPaths().site)
	if e = writeJSON(m.tlsPaths().config, c, 0600, -1); e != nil {
		return recoverSite(e)
	}
	if !purge {
		return nil
	}
	for path := range c.Resources {
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	if e = m.nginxReload(ctx); e != nil {
		return e
	}
	if _, e = m.sys(ctx, "daemon-reload"); e != nil {
		return e
	}
	if e = os.RemoveAll(m.tlsPaths().root); e != nil {
		return e
	}
	if e = os.RemoveAll(m.tlsPaths().webroot); e != nil {
		return e
	}
	if e = os.RemoveAll(filepath.Join(filepath.Dir(m.P.Releases), "acme")); e != nil {
		return e
	}
	return os.Remove(m.tlsPaths().config)
}

// A failed apt/bootstrap step has not enabled HTTPS. Clean only files whose exact
// generated contents prove ownership; a partial resource map must not prevent purge.
func (m *Manager) disableTLSProvisioning(ctx context.Context, c *TLSConfig, purge bool) error {
	if c.Enabled {
		return errors.New("HTTPS 半安装状态无效")
	}
	if e := m.verifyLEGO(c.Client); e != nil {
		return e
	}
	p := m.tlsPaths()
	expected := map[string]string{p.site: m.tlsSite(c, false, "")}
	for _, v := range []struct{ name, text string }{{tlsRenewUnit, tlsUnit("renew")}, {tlsReloadUnit, tlsUnit("reload")}, {tlsTimerUnit, tlsTimer}} {
		expected[filepath.Join(p.units, v.name)] = v.text
	}
	for path, text := range expected {
		if !exists(path) {
			continue
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil || real != path {
			return errors.New("发现外部 HTTPS 配置链接")
		}
		b, e := os.ReadFile(path)
		if e != nil || string(b) != text {
			return errors.New("半安装 HTTPS 资源被外部修改，拒绝清理")
		}
	}
	if exists(filepath.Join(p.units, tlsTimerUnit)) {
		if _, e := m.sys(ctx, "daemon-reload"); e != nil {
			return e
		}
		for _, u := range []string{tlsRenewUnit, tlsReloadUnit, tlsTimerUnit} {
			if !exists(filepath.Join(p.units, u)) {
				continue
			}
			b, e := m.sys(ctx, "show", u, "-p", "FragmentPath", "--value")
			if e != nil || strings.TrimSpace(string(b)) != filepath.Join(p.units, u) {
				return errors.New("HTTPS 实际 unit 路径不符")
			}
			b, e = m.sys(ctx, "show", u, "-p", "DropInPaths", "--value")
			if e != nil || strings.TrimSpace(string(b)) != "" {
				return errors.New("发现外部 HTTPS systemd drop-in")
			}
		}
		if _, e := m.sys(ctx, "disable", "--now", tlsTimerUnit); e != nil {
			return e
		}
	}
	if busy, e := m.tlsOperation(ctx); e != nil {
		return e
	} else if busy != "" {
		return ErrTLSBusy
	}
	hadSite := exists(p.site)
	for path := range expected {
		if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	if _, e := m.sys(ctx, "daemon-reload"); e != nil {
		return e
	}
	if hadSite {
		if _, e := m.sys(ctx, "is-active", "--quiet", "nginx.service"); e == nil {
			if e = m.nginxReload(ctx); e != nil {
				return e
			}
		}
	}
	if !purge {
		c.Resources = map[string]string{}
		return writeJSON(p.config, c, 0600, -1)
	}
	for _, dir := range []string{p.root, p.webroot, filepath.Join(filepath.Dir(m.P.Releases), "acme")} {
		if e := os.RemoveAll(dir); e != nil {
			return e
		}
	}
	return os.Remove(p.config)
}
