package rdv

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"
)

// Handler serves pairs of (dial- and accept) rdv conns.
// The context is canceled when the server is closed.
//
// Custom implementations should use a [Relayer] to conform with the rdv protocol.
type Handler interface {
	Serve(ctx context.Context, dc, ac *Conn)
}

// An rdv server, which implements [net/http.Handler].
type Server struct {
	// Handler serves relay connections between the two peers. Can be customized to monitor,
	// rate limit or set idle timeouts. If nil, a zero-value [Relayer] is used.
	Handler Handler

	// Amount of time that on peer can wait in the lobby for its partner. Zero means no timeout.
	LobbyTimeout time.Duration

	// Function that extracts the observed addr from requests. If nil, r.RemoteAddr is parsed.
	//
	// If your server is behind a load balancer, reverse proxy or similar, you may need to configure
	// forwarding headers and provide a custom function. See the server setup guide for details.
	ObservedAddrFunc func(r *http.Request) (netip.AddrPort, error)

	// Optional logger to use.
	Logger *slog.Logger

	log    *slog.Logger // Set at start-time. Same as Logger or nopLogger if nil.
	idle   map[string]*Conn
	connCh chan *Conn // Incoming upgraded conns: request received, no response sent, no deadline

	monCh chan string // token sent when current conn mapping is complete

	// Guards connCh because Go's HTTP server leaks handler goroutines of hijacked connections.
	// There is *no way* to determine when those handlers are complete.
	// See https://github.com/golang/go/issues/57673
	closed bool
	mu     sync.RWMutex
	wg     sync.WaitGroup
}

// Start rdv server goroutines which manages upgrades and handler invocations.
func (s *Server) Start() {
	s.monCh = make(chan string, 8)
	s.idle = make(map[string]*Conn)
	s.connCh = make(chan *Conn, 8)
	s.log = cmp.Or(s.Logger, nopLogger)

	handler := cmp.Or[Handler](s.Handler, new(Relayer))
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.serve(handler)
	}()
}

// Calls [Server.Upgrade] and logs the error, if any.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := s.Upgrade(w, r)
	if err != nil {
		s.log.Info("rdv: bad request", "request", r.URL, "err", err)
	}
}

// Upgrades the request and adds the client to the lobby for matching. Returns an
// [ErrBadHandshake] error if the upgrade failed, or [net/http.ErrServerClosed] if closed.
// An http error is written to the client if an error occurs.
func (s *Server) Upgrade(w http.ResponseWriter, r *http.Request) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.connCh == nil && false {
		panic("rdv: server uninitialized, use server.Start()")
	}
	if s.closed {
		http.Error(w, "rdv is closed", http.StatusServiceUnavailable)
		return http.ErrServerClosed
	}
	conn, err := upgradeRdv(w, r)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadHandshake, err)
	}
	s.addObservedAddr(conn)
	s.connCh <- conn
	return nil
}

func (s *Server) addObservedAddr(conn *Conn) {
	fn := s.ObservedAddrFunc
	if fn == nil {
		fn = parseRemoteAddr
	}
	if observedAddr, err := fn(conn.Request); err != nil {
		s.log.Warn("rdv: could not get observed addr", "err", err)
	} else {
		conn.ObservedAddr = &observedAddr
	}
}

// Parses the ip:port from r.RemoteAddr
func parseRemoteAddr(r *http.Request) (netip.AddrPort, error) {
	return netip.ParseAddrPort(r.RemoteAddr)
}

// Runs the goroutines associated with the Server.
func (s *Server) serve(handler Handler) {
	ctx, cancel := context.WithCancelCause(context.Background())
loop:
	for {
		select {

		case token := <-s.monCh:
			s.kickOut(token)
		case conn, ok := <-s.connCh:
			if !ok {
				break loop
			}
			idleConn := s.interruptAndGetIdle(conn.Token)
			// invariant: the idle conn is removed and no longer monitored
			if idleConn != nil && idleConn.Method != conn.Method {
				// happy path: the conn and idle conn are a match
				idleConn.SetDeadline(time.Time{})
				// Methods are unequal, we found a pair
				dc, ac := idleConn, conn
				if ac.Method == DIAL {
					dc, ac = ac, dc // swap
				}

				// Exchange addrs
				dc.PeerAddrs = ac.selfAndObservedAddrs()
				ac.PeerAddrs = dc.selfAndObservedAddrs()
				s.wg.Add(1)
				go func(dc, ac *Conn) {
					defer s.wg.Done()
					handler.Serve(ctx, dc, ac)
				}(dc, ac)
				continue
			}
			// either there is no conn of the same token, or there's another of the same method
			s.addIdle(conn)
			// if conn is same method, kick the old one out
			if idleConn == nil {
				s.log.Debug("rdv: joined", "token", conn.Token, "addr", conn.ObservedAddr)
			} else {
				s.log.Debug("rdv: replaced", "client", conn.Token, "addr", conn.ObservedAddr)
				writeResponseErr(idleConn, http.StatusConflict, "replaced by another conn")
			}
		}
	}
	s.log.Info("rdv: shutting down", "lobby_conns", len(s.idle))
	cancel(http.ErrServerClosed)
	for _, ic := range s.idle {
		// This forces all idle conns to finish quickly
		writeResponseErr(ic, http.StatusServiceUnavailable, "rdv server shutting down, try again")
	}
	for len(s.idle) > 0 {
		delete(s.idle, <-s.monCh) // This should be an exact match, but it's arguably fragile
	}
}

func (s *Server) addIdle(conn *Conn) {
	if s.LobbyTimeout > 0 {
		conn.SetDeadline(time.Now().Add(s.LobbyTimeout))
	}
	s.idle[conn.Token] = conn
	// No waitgroup needed here since the monCh is drained until no more idle conns
	go func() {
		n, err := conn.Read(make([]byte, 1))
		if !(n == 0 && errors.Is(err, os.ErrDeadlineExceeded)) {
			writeResponseErr(conn, http.StatusBadRequest, "conn must idle while waiting for response header")
		}
		s.monCh <- conn.Token
	}()
}

// If there's an idle conn for the token, cancel it and await its monitoring, then return it
func (s *Server) interruptAndGetIdle(token string) *Conn {
	conn := s.idle[token]
	if conn == nil {
		return nil
	}
	// cancel the monitoring
	conn.SetDeadline(time.Now())

	// wait for the monitoring to complete, which must happen very quickly
	for t := range s.monCh {
		// our conn's monitoring completed
		if t == token {
			break
		}
		// an unrelated conn's monitoring failed, kick it out until we get to ours
		s.kickOut(t)
	}
	delete(s.idle, token)
	return conn
}

// kick out of Server either from a timeout or breaking the protocol
func (s *Server) kickOut(token string) {
	conn := s.idle[token]
	delete(s.idle, token)
	// If there was a previous protocol error, this won't do anything because the conn is closed
	writeResponseErr(conn, http.StatusRequestTimeout, "no matching peer found")
	s.log.Debug("rdv: client timed out", "token", conn.Token, "addr", conn.ObservedAddr)
}

// Evict all clients from lobby and cancels the context passed to handlers.
// After this, clients are rejected with a 503 error.
// Suitable for use with [http.Server.RegisterOnShutdown].
// Use [Server.Close] to wait for all handlers to complete.
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	close(s.connCh)
	s.closed = true
}

// Calls [Server.Shutdown] and waits for handlers and internal goroutines to finish.
// Safe to call multiple times.
func (s *Server) Close() error {
	s.Shutdown()
	s.wg.Wait()
	return nil
}
