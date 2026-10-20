package api

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// Both directions authenticate their peer. /run/lock is commonly mode 1777;
// checking only the connecting client would let an unprivileged process claim
// the socket name while the manager is stopped and receive CLI secrets.
func rootPeer(c *net.UnixConn) bool {
	raw, e := c.SyscallConn()
	if e != nil {
		return false
	}
	var credential *syscall.Ucred
	var socketError error
	e = raw.Control(func(fd uintptr) {
		credential, socketError = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return e == nil && socketError == nil && credential != nil && credential.Uid == 0
}
func dialRoot(ctx context.Context, path string) (net.Conn, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if e != nil {
		return nil, e
	}
	unix, ok := c.(*net.UnixConn)
	if !ok || !rootPeer(unix) {
		c.Close()
		return nil, errors.New("管理 socket 服务端不是 root")
	}
	return c, nil
}
