package config

import "net"

// DualStack reports whether the host can bind the IPv6 wildcard. On Linux with
// the default bindv6only=0 that socket also accepts IPv4 connections.
func DualStack() bool {
	l, e := net.Listen("tcp", "[::]:0")
	if e != nil {
		return false
	}
	l.Close()
	return true
}

// Wildcard is the widest bindable address: "::" when IPv6 is available,
// otherwise "0.0.0.0" for kernels booted with IPv6 disabled.
func Wildcard() string {
	if DualStack() {
		return "::"
	}
	return "0.0.0.0"
}

// DefaultListen is the listener address for newly generated configurations.
func DefaultListen() string { return net.JoinHostPort(Wildcard(), "1080") }
