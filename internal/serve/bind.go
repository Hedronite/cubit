package serve

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// RefusedError is a bind the operator did not opt into. Its text is the whole
// stderr line: what happened, then the fix.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

// ListenError is a listen that failed.
type ListenError struct{ Msg string }

func (e *ListenError) Error() string { return e.Msg }

const loopbackExample = "a loopback address such as 127.0.0.1:9101"

func refuse(format string, a ...any) error {
	return &RefusedError{Msg: fmt.Sprintf(format, a...)}
}

// resolver looks a host name up. It is a variable so a test can answer.
var resolver = func(ctx context.Context, host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// CheckBind applies the bind rule: the host must be a loopback IP or a name
// that resolves only to loopback addresses. Anything else, including an empty
// host and the unspecified addresses, needs unsafe.
func CheckBind(ctx context.Context, addr string, unsafe bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return refuse("--listen %s is not host:port; use a loopback address such as 127.0.0.1:9101", addr)
	}
	if unsafe {
		return nil
	}
	const optIn = "pass --unsafe-listen to allow that (the metrics are unauthenticated), or use " + loopbackExample
	if host == "" {
		return refuse("--listen %s has no host and would bind every interface; %s", addr, optIn)
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return nil
		case ip.To4() == nil && ip.IsUnspecified():
			return refuse("--listen %s binds every interface; %s", addr, optIn)
		}
		return refuse("--listen %s is not a loopback address; pass --unsafe-listen to bind it anyway (the metrics are unauthenticated), or use %s", addr, loopbackExample)
	}
	if host != "localhost" {
		return refuse("--listen %s is not a loopback address; pass --unsafe-listen to bind it anyway (the metrics are unauthenticated), or use %s", addr, loopbackExample)
	}
	ips, err := resolver(ctx, host)
	if err != nil || len(ips) == 0 {
		return refuse("--listen %s cannot be resolved (%v); use %s", addr, err, loopbackExample)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return refuse("--listen %s resolves to %s, which is not a loopback address; %s", addr, ip, optIn)
		}
	}
	return nil
}

// listen opens the listener, turning a failure into one line that names the fix.
func listen(addr string) (net.Listener, error) {
	l, err := net.Listen("tcp", addr)
	if err == nil {
		return l, nil
	}
	reason := err.Error()
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		reason = oe.Err.Error()
	}
	fix := "check the address"
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		fix = "stop the process using that port, or pass --listen with a free port"
	case errors.Is(err, syscall.EACCES):
		fix = "use a port above 1024, or run cubit with the capability to bind low ports"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		fix = "use an address this host owns, such as 127.0.0.1"
	}
	return nil, &ListenError{Msg: fmt.Sprintf("cannot listen on %s: %s; %s", addr, reason, fix)}
}
