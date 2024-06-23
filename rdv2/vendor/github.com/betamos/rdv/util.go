package rdv

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"time"
)

func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// A low-overhead idle timer that intercepts write calls to extend the deadline continously
type idleTimer struct {
	timeout time.Duration
	timer   *time.Timer
}

func newIdleTimer(timeout time.Duration, cb func(err error)) *idleTimer {
	return &idleTimer{timeout, time.AfterFunc(timeout, func() { cb(context.DeadlineExceeded) })}
}

// Registers activity and prolongs the deadline
func (t *idleTimer) Write(p []byte) (int, error) {
	t.timer.Reset(t.timeout)
	return len(p), nil
}

func (t *idleTimer) Stop() {
	t.timer.Stop()
}

// Unwraps any net.OpError to prevent address noise
func unwrapOp(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err
	}
	return err
}

// An [slog.Handler] that logs nothing.
// TODO(https://github.com/golang/go/issues/62005): Use std lib
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// An [slog.Logger] that logs nothing.
var nopLogger = slog.New(discardHandler{})
