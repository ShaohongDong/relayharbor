// Package socks implements the private SOCKS handshake and end-to-end probes.
package socks

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/ShaohongDong/relayharbor/internal/config"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const Connect byte = 1
const UDPTunnel byte = 0xf3

var ErrProtocol = errors.New("SOCKS5 协议或上游连接失败")
var Failure = []byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0}

func read(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	_, e := io.ReadFull(r, b)
	return b, e
}
func Address(host string, port int) []byte {
	b := []byte{}
	if ip := net.ParseIP(host); ip != nil {
		if v := ip.To4(); v != nil {
			b = append([]byte{1}, v...)
		} else {
			b = append([]byte{4}, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil
		}
		b = append([]byte{3, byte(len(host))}, []byte(host)...)
	}
	return binary.BigEndian.AppendUint16(b, uint16(port))
}
func ReadAddress(r io.Reader) ([]byte, error) {
	k, e := read(r, 1)
	if e != nil {
		return nil, e
	}
	n := 0
	switch k[0] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		l, e := read(r, 1)
		if e != nil || l[0] == 0 {
			return nil, ErrProtocol
		}
		k = append(k, l...)
		n = int(l[0])
	default:
		return nil, ErrProtocol
	}
	b, e := read(r, n+2)
	return append(k, b...), e
}
func Authenticate(c net.Conn, a config.Auth) error {
	if _, e := c.Write([]byte{5, 1, 2}); e != nil {
		return e
	}
	b, e := read(c, 2)
	if e != nil || string(b) != "\x05\x02" {
		return ErrProtocol
	}
	p := append([]byte{1, byte(len(a.Username))}, []byte(a.Username)...)
	p = append(p, byte(len(a.Password)))
	p = append(p, []byte(a.Password)...)
	if _, e = c.Write(p); e != nil {
		return e
	}
	b, e = read(c, 2)
	if e != nil || string(b) != "\x01\x00" {
		return ErrProtocol
	}
	return nil
}
func Accept(c net.Conn, a config.Auth) error {
	b, e := read(c, 2)
	if e != nil || b[0] != 5 {
		return ErrProtocol
	}
	methods, e := read(c, int(b[1]))
	if e != nil {
		return e
	}
	ok := false
	for _, m := range methods {
		if m == 2 {
			ok = true
		}
	}
	if !ok {
		_, _ = c.Write([]byte{5, 255})
		return ErrProtocol
	}
	if _, e = c.Write([]byte{5, 2}); e != nil {
		return e
	}
	b, e = read(c, 2)
	if e != nil || b[0] != 1 {
		return ErrProtocol
	}
	u, e := read(c, int(b[1]))
	if e != nil {
		return e
	}
	b, e = read(c, 1)
	if e != nil {
		return e
	}
	p, e := read(c, int(b[0]))
	if e != nil {
		return e
	}
	if subtle.ConstantTimeCompare(u, []byte(a.Username)) != 1 || subtle.ConstantTimeCompare(p, []byte(a.Password)) != 1 {
		_, _ = c.Write([]byte{1, 1})
		return ErrProtocol
	}
	_, e = c.Write([]byte{1, 0})
	return e
}
func Request(c net.Conn, command byte, addr []byte) ([]byte, error) {
	if _, e := c.Write(append([]byte{5, command, 0}, addr...)); e != nil {
		return nil, e
	}
	h, e := read(c, 3)
	if e != nil {
		return nil, e
	}
	b, e := ReadAddress(c)
	if e != nil || string(h) != "\x05\x00\x00" {
		return nil, ErrProtocol
	}
	return append(h, b...), nil
}
func Dial(ctx context.Context, addr string, auth config.Auth, command byte, target []byte) (net.Conn, []byte, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if e != nil {
		return nil, nil, e
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if e = Authenticate(c, auth); e != nil {
		c.Close()
		return nil, nil, e
	}
	reply, e := Request(c, command, target)
	if e != nil {
		c.Close()
		return nil, nil, e
	}
	if e = ctx.Err(); e != nil {
		c.Close()
		return nil, nil, e
	}
	_ = c.SetDeadline(time.Time{})
	return c, reply, nil
}

type ProbeResult struct {
	Healthy   bool     `json:"healthy"`
	LatencyMS *float64 `json:"latencyMs"`
}

func Probe(ctx context.Context, addr string, a config.Auth, p config.Probe) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout*float64(time.Second)))
	defer cancel()
	host, port := "", 0
	var u *url.URL
	if p.Type == "tcp" {
		h, ps, _ := net.SplitHostPort(p.Addr)
		host = h
		port, _ = strconv.Atoi(ps)
	} else {
		u, _ = url.Parse(p.URL)
		host = u.Hostname()
		port = 80
		if u.Scheme == "https" {
			port = 443
		}
		if u.Port() != "" {
			port, _ = strconv.Atoi(u.Port())
		}
	}
	c, _, e := Dial(ctx, addr, a, Connect, Address(host, port))
	if e != nil {
		return ProbeResult{}
	}
	defer c.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(d)
	}
	raw := c
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	if u != nil {
		if u.Scheme == "https" {
			tlsConn := tls.Client(c, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
			if e = tlsConn.HandshakeContext(ctx); e != nil {
				return ProbeResult{}
			}
			c = tlsConn
		}
		req := &http.Request{Method: "GET", URL: u, Host: u.Host, Header: make(http.Header), Close: true}
		if e = req.Write(c); e != nil {
			return ProbeResult{}
		}
		resp, e := http.ReadResponse(bufio.NewReader(c), req)
		if e != nil {
			return ProbeResult{}
		}
		good := resp.StatusCode == p.ExpectedStatus
		c.Close()
		resp.Body.Close()
		if !good {
			return ProbeResult{}
		}
	}
	ms := float64(time.Since(start).Microseconds()) / 1000
	return ProbeResult{true, &ms}
}
func Target(addr string) []byte {
	h, p, e := net.SplitHostPort(addr)
	if e != nil {
		panic(fmt.Sprint(e))
	}
	n, _ := strconv.Atoi(p)
	return Address(h, n)
}
