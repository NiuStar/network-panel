//go:build windows
// +build windows

// Source: https://github.com/zhanghanyun/backtrace (v1.0.8)
// Note: Windows does not support the raw socket tracer used by backtrace.
package backtrace

import (
	"context"
	"errors"
	"net"
	"time"
)

// ErrUnsupportedPlatform indicates backtrace is unavailable on Windows builds.
var ErrUnsupportedPlatform = errors.New("backtrace: unsupported on windows")

// Config is a configuration for Tracer.
type Config struct {
	Delay    time.Duration
	Timeout  time.Duration
	MaxHops  int
	Count    int
	Networks []string
	Addr     *net.IPAddr
}

// Tracer is a placeholder tracer for Windows builds.
type Tracer struct {
	Config
}

// Trace is not supported on Windows builds.
func (t *Tracer) Trace(ctx context.Context, ip net.IP, h func(reply *Reply)) error {
	return ErrUnsupportedPlatform
}

// NewSession is not supported on Windows builds.
func (t *Tracer) NewSession(ip net.IP) (*Session, error) {
	return nil, ErrUnsupportedPlatform
}

// Close is a no-op on Windows builds.
func (t *Tracer) Close() {}

// Session is a placeholder session for Windows builds.
type Session struct{}

// Close is a no-op on Windows builds.
func (s *Session) Close() {}

// Ping is not supported on Windows builds.
func (s *Session) Ping(ttl int) error { return ErrUnsupportedPlatform }

// Receive returns a closed channel on Windows builds.
func (s *Session) Receive() <-chan *Reply {
	ch := make(chan *Reply)
	close(ch)
	return ch
}

// Reply is a stub reply on Windows builds.
type Reply struct {
	IP   net.IP
	RTT  time.Duration
	Hops int
}

// Node is a detected network node.
type Node struct {
	IP  net.IP
	RTT []time.Duration
}

// Hop is a set of detected nodes.
type Hop struct {
	Nodes    []*Node
	Distance int
}

// DefaultConfig is the default configuration for Tracer.
var DefaultConfig = Config{
	Delay:    50 * time.Millisecond,
	Timeout:  500 * time.Millisecond,
	MaxHops:  15,
	Count:    1,
	Networks: []string{},
}

// DefaultTracer is a tracer with DefaultConfig.
var DefaultTracer = &Tracer{
	Config: DefaultConfig,
}

// Trace is a simple traceroute tool using DefaultTracer.
func Trace(ip net.IP) ([]*Hop, error) {
	return nil, ErrUnsupportedPlatform
}
