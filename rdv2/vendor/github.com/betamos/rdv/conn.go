package rdv

import (
	"bufio"
	"net"
	"net/http"
)

// Hide the embedding to prevent misuse
type netConn net.Conn

// Conn is an rdv conn, either p2p or relay, which implements [net.Conn].
type Conn struct {
	netConn
	br *bufio.Reader

	// Metadata about the rdv conn.
	*Meta

	// Reports whether the conn is relayed by an rdv server. Client conns only.
	IsRelay bool

	// Read-only http request. Server conns only.
	Request *http.Request
}

func newDirectConn(nc net.Conn, meta *Meta) *Conn {
	return &Conn{
		netConn: nc,
		br:      bufio.NewReader(nc),
		IsRelay: false,
		Meta:    meta,
	}
}

func newRelayConn(nc net.Conn, br *bufio.Reader, meta *Meta, req *http.Request) *Conn {
	return &Conn{
		netConn: nc,
		br:      br,
		IsRelay: true,
		Meta:    meta,
		Request: req,
	}
}

func (c *Conn) Read(p []byte) (int, error) {
	return c.br.Read(p)
}
