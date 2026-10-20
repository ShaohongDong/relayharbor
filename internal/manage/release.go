package manage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const DefaultVersion = "3.3.0"

var Pinned = map[string]string{"amd64": "676fb7f78d267b6ae73df719c0c7f2b565dde7147da935cfafbc1e1da558b6d5", "arm64": "d03699e3f385d4ff5dad68046712adfcc7515325a064d2ab046e0bece30f8f8f"}
var versionRE = regexp.MustCompile(`^v?([0-9]+\.[0-9]+\.[0-9]+)$`)

func Version(v string) (string, error) {
	s := versionRE.FindStringSubmatch(v)
	if s == nil {
		return "", errors.New("版本必须为正式 x.y.z")
	}
	return s[1], nil
}

type Release struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Binary  string `json:"binary"`
	SHA256  string `json:"sha256"`
}

func Download(ctx context.Context, url, path string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return errors.New("下载地址无效")
	}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("发布包下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("发布包下载失败")
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, 512<<20))
	closeErr := f.Close()
	if copyErr != nil {
		return errors.New("发布包下载不完整")
	}
	return closeErr
}
func Checksum(path, filename string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	sum := ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == filename {
			if sum != "" || len(f[0]) != 64 {
				return "", errors.New("校验清单无效")
			}
			sum = strings.ToLower(f[0])
		}
	}
	if sum == "" {
		return "", errors.New("校验清单缺少对应发布包")
	}
	return sum, nil
}
func Extract(archive, dest string) error {
	f, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return errors.New("发布包格式无效")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("发布包格式无效")
		}
		if h.Name != "gost" && h.Name != "./gost" {
			continue
		}
		if found || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 256<<20 {
			return errors.New("发布包二进制无效")
		}
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if e != nil {
			return e
		}
		_, e = io.CopyN(out, tr, h.Size)
		ce := out.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		found = true
	}
	if !found {
		return errors.New("发布包没有 gost")
	}
	return nil
}
func (m *Manager) VerifyRelease(r Release) error {
	real, e := filepath.EvalSymlinks(r.Binary)
	if e != nil || !within(real, m.P.Releases) || real != r.Binary {
		return errors.New("二进制路径不属于受管发布目录")
	}
	d, e := Digest(real)
	if e != nil || d != r.SHA256 {
		return errors.New("GOST 二进制校验失败")
	}
	return nil
}
func (m *Manager) PrepareRelease(ctx context.Context, version, archive, sums string) (Release, error) {
	v, e := Version(version)
	if e != nil {
		return Release{}, e
	}
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return Release{}, errors.New("只支持 amd64/arm64")
	}
	if (archive == "") != (sums == "") {
		return Release{}, errors.New("离线发布包和校验清单必须一起提供")
	}
	dir := filepath.Join(m.P.Releases, v, arch)
	if e = os.MkdirAll(dir, 0755); e != nil {
		return Release{}, e
	}
	for _, p := range []string{filepath.Dir(m.P.Releases), m.P.Releases, filepath.Dir(dir), dir} {
		if e = os.Chmod(p, 0755); e != nil {
			return Release{}, e
		}
	}
	tmp, e := os.MkdirTemp(dir, ".candidate-*")
	if e != nil {
		return Release{}, e
	}
	defer os.RemoveAll(tmp)
	filename := fmt.Sprintf("gost_%s_linux_%s.tar.gz", v, arch)
	if archive == "" {
		base := "https://github.com/go-gost/gost/releases/download/v" + v + "/"
		archive = filepath.Join(tmp, filename)
		sums = filepath.Join(tmp, "checksums.txt")
		if e = Download(ctx, base+filename, archive); e != nil {
			return Release{}, e
		}
		if e = Download(ctx, base+"checksums.txt", sums); e != nil {
			return Release{}, e
		}
	}
	want, e := Checksum(sums, filename)
	if e != nil {
		return Release{}, e
	}
	got, e := Digest(archive)
	if e != nil || got != want || (v == DefaultVersion && want != Pinned[arch]) {
		return Release{}, errors.New("发布包 SHA-256 校验失败")
	}
	binary := filepath.Join(tmp, "gost")
	if e = Extract(archive, binary); e != nil {
		return Release{}, e
	}
	b, e := m.Exec(ctx, binary, "-V")
	versionFields := strings.Fields(string(b))
	if e != nil || len(versionFields) < 2 || versionFields[0] != "gost" || strings.TrimPrefix(versionFields[1], "v") != v || !strings.Contains(string(b), "linux/"+arch+")") {
		return Release{}, errors.New("发布包版本或架构不符")
	}
	hash, e := Digest(binary)
	if e != nil {
		return Release{}, e
	}
	dest := filepath.Join(dir, "gost")
	if !exists(dest) {
		if e = os.Rename(binary, dest); e != nil {
			return Release{}, e
		}
	}
	release := Release{v, arch, dest, hash}
	if e = m.VerifyRelease(release); e != nil {
		return Release{}, e
	}
	return release, nil
}
func (m *Manager) ValidateNative(ctx context.Context, r Release, c any) error {
	dir, e := os.MkdirTemp("", "gost-config-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config.json")
	if e = writeJSON(path, c, 0600, -1); e != nil {
		return e
	}
	if _, e = m.Exec(ctx, r.Binary, "-C", path, "-O", "json"); e != nil {
		return errors.New("GOST 原生配置验证失败")
	}
	return nil
}
