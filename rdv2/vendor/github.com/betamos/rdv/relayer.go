package rdv

import (
	"cmp"
	"context"
	"errors"
	"io"
	"math"
	"time"
)

// Relayer handles a pair of rdv conns by relaying data between them. The zero-value can be used.
type Relayer struct {
	// Specifies a duration of inactivity after which the relay is closed.
	// If zero, there is no timeout.
	IdleTimeout time.Duration

	// The size of copy buffers. By default the [io.Copy] default size is used.
	BufferSize int
}

// Write an http error and close both conns.
func (r *Relayer) Reject(dc, ac *Conn, statusCode int, reason string) error {
	return errors.Join(
		writeResponseErr(dc, statusCode, reason),
		writeResponseErr(ac, statusCode, reason))
}

// Serve implements [Handler] by connecting and relaying data between peers as necessary.
// Call [Relayer.Continue] and [Relayer.Relay] manually for custom behavior, monitoring,
// rate-limiting, etc.
func (r *Relayer) Serve(ctx context.Context, dc, ac *Conn) {
	err := r.Continue(ctx, dc, ac)
	if err != nil {
		return
	}
	r.Relay(ctx, ac, dc, dc, ac) // From ac -> dc and dc -> ac
}

// Sends the http upgrade response to both conns and reads the dialer's CONTINUE command.
// Returns [ErrOther] if a p2p conn was established.
func (r *Relayer) Continue(ctx context.Context, dc, ac *Conn) error {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(r.IdleTimeout, math.MaxInt64))
	defer cancel()

	stop := context.AfterFunc(ctx, func() {
		dc.Close()
		ac.Close()
	})
	err := r._continue(dc, ac)
	if err != nil {
		return err
	}
	stop()
	return nil
}

// Sends the http upgrade response to both conns and reads the dialer's CONTINUE command.
// Returns [ErrOther] if a p2p conn was established.
func (r *Relayer) _continue(dc, ac *Conn) (err error) {
	if err = newRdvResponse(dc.Meta).Write(dc); err != nil {
		return
	}
	if err = newRdvResponse(ac.Meta).Write(ac); err != nil {
		return
	}
	// Forward the continue command from dialer to accepter
	err = readCmdContinue(dc.br)
	if err != nil {
		return
	}
	return writeCmdContinue(ac)
}

// Copies data from r1 to w1 and from r2 to w2. Both writers are closed upon an IO error,
// when ctx is canceled, or due to inactivity (see [Relayer.IdleTimeout]).
// Returns amount of data copied for each pair, and the first error that occurred, often [io.EOF].
// Note that [Relayer.Continue] must be called beforehand.
//
// In order to monitor or rate-limit conns, use [io.TeeReader] for r1 and r2.
func (r *Relayer) Relay(ctx context.Context, w1, w2 io.WriteCloser, r1, r2 io.Reader) (n1 int64, n2 int64, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	context.AfterFunc(ctx, func() {
		w1.Close()
		w2.Close()
	})
	it := newIdleTimer(cmp.Or(r.IdleTimeout, math.MaxInt64), cancel)
	defer it.Stop()

	r1 = io.TeeReader(r1, it)
	r2 = io.TeeReader(r2, it)

	var buf1, buf2 []byte
	if r.BufferSize > 0 {
		buf1 = make([]byte, r.BufferSize)
		buf2 = make([]byte, r.BufferSize)
	}

	// Use a single extra goroutine in order to reduce memory overhead.
	n2Ch := make(chan int64)
	go func() { n2Ch <- copyCancel(w2, r2, buf2, cancel) }()
	n1 = copyCancel(w1, r1, buf1, cancel)
	n2 = <-n2Ch
	err = context.Cause(ctx)
	return
}

func copyCancel(w io.Writer, r io.Reader, buf []byte, cancel context.CancelCauseFunc) int64 {
	n, err := io.CopyBuffer(w, r, buf)
	if err == nil {
		err = io.EOF
	}
	cancel(err)
	return n
}
