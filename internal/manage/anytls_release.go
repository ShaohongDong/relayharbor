package manage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const SingBoxVersion = "1.14.3"

var SingBoxPinned = map[string]string{
	"amd64": "e2bdf179c15a3652955dc44867e33ca0965c9c9b91b34267dcc5a5d639a5feee",
	"arm64": "29dae24d76bea3bc62d07dd3e60cfc12003de7debbd6b768df9be9c68360174e",
}

func (m *Manager) verifySingBox(r Release) error {
	root := filepath.Join(filepath.Dir(m.P.Releases), "sing-box")
	real, e := filepath.EvalSymlinks(r.Binary)
	want := filepath.Join(root, SingBoxVersion, runtime.GOARCH, r.SHA256, "sing-box")
	if e != nil || real != r.Binary || real != want || r.Version != SingBoxVersion || r.Arch != runtime.GOARCH {
		return errors.New("AnyTLS 内核路径或版本不符")
	}
	st, e := os.Lstat(real)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return errors.New("AnyTLS 内核文件无效")
	}
	h, e := Digest(real)
	if e != nil || h != r.SHA256 {
		return errors.New("AnyTLS 内核校验失败")
	}
	return nil
}

func (m *Manager) prepareSingBox(ctx context.Context, archive string) (Release, error) {
	root := filepath.Join(filepath.Dir(m.P.Releases), "sing-box")
	if e := os.MkdirAll(root, 0755); e != nil {
		return Release{}, e
	}
	tmp, e := os.MkdirTemp(root, ".download-")
	if e != nil {
		return Release{}, e
	}
	defer os.RemoveAll(tmp)
	arch := runtime.GOARCH
	want := SingBoxPinned[arch]
	if want == "" {
		return Release{}, errors.New("不支持的 AnyTLS 架构")
	}
	base := "sing-box-" + SingBoxVersion + "-linux-" + arch
	if archive == "" {
		archive = filepath.Join(tmp, base+".tar.gz")
		if e = Download(ctx, "https://github.com/SagerNet/sing-box/releases/download/v"+SingBoxVersion+"/"+base+".tar.gz", archive); e != nil {
			return Release{}, e
		}
	}
	h, e := Digest(archive)
	if e != nil || h != want {
		return Release{}, errors.New("sing-box 官方发布包 SHA-256 不符")
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
	bin := filepath.Join(tmp, "sing-box")
	found := false
	for {
		entry, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return Release{}, e
		}
		if entry.Name != base+"/sing-box" {
			continue
		}
		if found || entry.Typeflag != tar.TypeReg || entry.Size <= 0 || entry.Size > 256<<20 {
			return Release{}, errors.New("sing-box 发布包内容无效")
		}
		found = true
		out, e := os.OpenFile(bin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if e != nil {
			return Release{}, e
		}
		_, copyErr := io.Copy(out, tr)
		e = errors.Join(copyErr, out.Close())
		if e != nil {
			return Release{}, e
		}
	}
	if !found {
		return Release{}, errors.New("发布包缺少 sing-box")
	}
	h, e = Digest(bin)
	if e != nil {
		return Release{}, e
	}
	r := Release{SingBoxVersion, arch, filepath.Join(root, SingBoxVersion, arch, h, "sing-box"), h}
	if e = os.MkdirAll(filepath.Dir(r.Binary), 0755); e != nil {
		return Release{}, e
	}
	// Management services use umask 0077; the unprivileged worker must still
	// traverse and execute the verified, non-secret release.
	for _, p := range []string{root, filepath.Join(root, SingBoxVersion), filepath.Join(root, SingBoxVersion, arch), filepath.Dir(r.Binary)} {
		if real, e := filepath.EvalSymlinks(p); e != nil || real != p {
			return Release{}, errors.New("AnyTLS 内核目录包含符号链接")
		}
		if e = os.Chmod(p, 0755); e != nil {
			return Release{}, e
		}
	}
	if e = os.Chmod(bin, 0755); e != nil {
		return Release{}, e
	}
	if !exists(r.Binary) {
		if e = os.Rename(bin, r.Binary); e != nil {
			return Release{}, e
		}
	}
	return r, m.verifySingBox(r)
}
