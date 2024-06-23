package rdv

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// Client can dial and accept rdv conns. The zero-value is valid.
type Client struct {
	// Can be used to allow only a certain set of spaces, such as public IPs only. Defaults to
	// DefaultSpaces which optimal for both LAN and WAN connectivity.
	AddrSpaces AddrSpace

	// Picker used by the dialing side. If nil, defaults to WaitForP2P(time.Second)
	Picker Picker

	// Timeout for the full dial/accept process, if provided. Note this may include DNS, TLS,
	// signaling delay and probing for p2p. We recommend >3s in production.
	Timeout time.Duration

	// Custom TLS config to use with the rdv server.
	TlsConfig *tls.Config

	// Optional logger to use.
	Logger *slog.Logger
}

// Dial a peer, shorthand for Do(ctx, DIAL, ...)
func (c *Client) Dial(ctx context.Context, addr, token string, header http.Header) (*Conn, *http.Response, error) {
	return c.Do(ctx, DIAL, addr, token, header)
}

// Accept a peer conn, shorthand for Do(ctx, ACCEPT, ...)
func (c *Client) Accept(ctx context.Context, addr, token string, header http.Header) (*Conn, *http.Response, error) {
	return c.Do(ctx, ACCEPT, addr, token, header)
}

// Connect with another peer through an rdv server endpoint.
//
//   - method: must be [DIAL] or [ACCEPT]
//   - addr: http(s) addr of the rdv server endpoint
//   - token: an arbitrary string for matching the two peers, typically chosen by the dialer
//   - header: an optional set of http headers included in the request, e.g. for authorization
//
// Returns an [ErrBadHandshake] error if the server doesn't upgrade the rdv conn properly.
// A read-only http response is returned if available, whether or not an error occurred.
func (c *Client) Do(ctx context.Context, method, addr, token string, header http.Header) (*Conn, *http.Response, error) {
	meta, err := newMeta(method, token)
	if err != nil {
		return nil, nil, err
	}
	var (
		log    = cmp.Or(c.Logger, nopLogger).With("token", meta.Token)
		spaces = cmp.Or(c.AddrSpaces, DefaultSpaces)
		picker = cmp.Or(c.Picker, WaitForP2P(time.Second))
	)
	if method == ACCEPT {
		picker = PickFirst()
	}
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(c.Timeout, math.MaxInt64))
	defer cancel()
	socket, err := newSocket(ctx, 0, c.TlsConfig)
	if err != nil {
		return nil, nil, err
	}
	laddrs := probeLocalAddrs(spaces)
	meta.SelfAddrs = selfAddrs(laddrs, socket.Port(), spaces)
	log.Debug("rdv: request", "method", meta.Method, "self_addrs", meta.SelfAddrs)

	relay, resp, err := dialRdvServer(ctx, socket, meta, addr, header)
	if err != nil {
		socket.Close()
		return nil, resp, err
	}

	log.Debug("rdv: response", "observed", meta.ObservedAddr, "peer_addrs", meta.PeerAddrs)
	ncs := make(chan *Conn)
	candidates := make(chan *Conn)
	go dialAndListen(ctx, log, laddrs, meta, socket, ncs)
	go clientHands(log, ncs, candidates)
	ncs <- relay // add relay conn here to prevent deadlock

	conns := picker.Pick(candidates, cancel)
	cancel()
	if len(conns) == 0 {
		return nil, resp, context.Cause(ctx)
	}
	chosen, err := clientShakes(log, conns)
	return chosen, resp, err
}

// Dial the rdv server and return a relay conn.
func dialRdvServer(ctx context.Context, socket *socket, meta *Meta, addr string, header http.Header) (*Conn, *http.Response, error) {
	// Force ipv4 to allow for zero-stun
	req, err := newRdvRequest(meta, addr, header)
	if err != nil {
		return nil, nil, err
	}
	nc, err := socket.DialURL4(ctx, req.URL)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() {
		nc.SetDeadline(time.Now())
	})
	defer stop()
	br := bufio.NewReader(nc)
	resp, err := doHttp(nc, br, req)
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	err = parseRdvResponse(meta, resp)
	if err != nil {
		slurp(resp, 1024)
		nc.Close()
		return nil, resp, err
	}
	return newRelayConn(nc, br, meta, req), nil, nil
}

// Dial and listen simultaneously to find a p2p match, until the context is canceled.
// Conns are sent to the out channel. This function takes ownership of the socket.
func dialAndListen(ctx context.Context, log *slog.Logger, laddrs map[AddrSpace]netip.Addr, meta *Meta, s *socket, out chan<- *Conn) {
	defer close(out)
	var wg sync.WaitGroup

	// Close the socket on ctx cancel, which triggers an accept error later
	wg.Add(1)
	context.AfterFunc(ctx, func() {
		s.Close()
		wg.Done()
	})
	for _, addr := range meta.PeerAddrs {
		space := AddrSpaceFrom(addr.Addr())
		laddr, ok := laddrs[space]
		if !ok {
			log.Debug("rdv: skip", "addr", addr, "space", space)
			continue
		}
		wg.Add(1)
		go func(addr netip.AddrPort) {
			defer wg.Done()
			nc, err := s.DialAddr(ctx, laddr, addr)
			if err != nil {
				log.Debug("rdv: dial err", "addr", addr, "err", unwrapOp(err))
				return
			}
			out <- newDirectConn(nc, meta)
		}(addr)
	}
	for {
		nc, err := s.Accept()
		if err != nil {
			break
		}
		addr := AddrPortFrom(nc.RemoteAddr())
		space := AddrSpaceFrom(addr.Addr())
		if _, ok := laddrs[space]; !ok {
			log.Debug("rdv: reject", "space", space, "addr", addr)
			nc.Close()
			continue
		}
		out <- newDirectConn(nc, meta)
	}
	wg.Wait()
	// success, otherwise relay
}

// Run the client "hand" part of the handshake for each conn in the in channel.
// Those that are successful are sent on the out channel.
func clientHands(log *slog.Logger, in <-chan *Conn, out chan<- *Conn) {
	defer close(out)
	var (
		cArr = []net.Conn{}
		wg   sync.WaitGroup
	)
	for conn := range in {
		cArr = append(cArr, conn)
		wg.Add(1)
		go func(conn *Conn) {
			defer wg.Done()
			err := clientHand(conn)
			if err != nil {
				log.Debug("rdv: shake err", "addr", conn.RemoteAddr(), "err", unwrapOp(err))
				conn.Close()
				return
			}
			log.Debug("rdv: shake ok", "addr", conn.RemoteAddr())

			out <- conn
		}(conn)
	}

	// Expire all deadlines, including those that finished
	t := time.Now()
	for _, c := range cArr {
		c.SetDeadline(t)
	}
	wg.Wait()
}

// Establishes candidate connections. The accepter should have at most one successful hand,
// but the dialer can have multiple.
func clientHand(c *Conn) error {
	if !c.IsRelay {
		if err := clientExchangeHeaders(c); err != nil {
			return err
		}
	}
	if c.Method == ACCEPT {
		return readCmdContinue(c.br)
	}
	return nil
}

// Finalizes the shake with conns[0] and returns it. The others are rejected and closed.
func clientShakes(log *slog.Logger, conns []*Conn) (*Conn, error) {
	chosen := conns[0]
	addr := AddrPortFrom(chosen.RemoteAddr())
	for _, conn := range conns[1:] {
		log.Debug("rdv: discard", "addr", conn.RemoteAddr())
		clientReject(conn, addr)
		conn.Close()
	}
	if err := clientShake(chosen); err != nil {
		chosen.Close()
		return nil, err
	}
	chosen.SetDeadline(time.Time{})
	return chosen, nil
}

// Finalizes candidate selection. Dialers write the confirm, whereas the listener do nothing
// (they already read the confirm earlier). Invoked at most once, IFF clientHand succeeded.
func clientShake(c *Conn) (err error) {
	c.SetDeadline(time.Now().Add(shortWriteTimeout))
	if c.Method == DIAL {
		err = writeCmdContinue(c)
	}
	return
}

// Writes an OTHER command if the conn is a relay.
func clientReject(c *Conn, other netip.AddrPort) error {
	if c.Method == DIAL && c.IsRelay {
		c.SetDeadline(time.Now().Add(shortWriteTimeout))
		return writeCmdOther(c, other)
	}
	return nil
}

// Direct conns should write and read the rdv header line
func clientExchangeHeaders(c *Conn) error {
	// Headers that should be written and read.
	self := header{DIAL, c.Token}
	peer := header{ACCEPT, c.Token}
	if c.Method == ACCEPT {
		self, peer = peer, self
	}
	if err := writeHeader(c, self); err != nil {
		return err
	}
	hdr, err := readHeader(c.br)
	if err != nil {
		return err
	}
	if *hdr != peer {
		return fmt.Errorf("unexpected header args")
	}
	return nil
}
