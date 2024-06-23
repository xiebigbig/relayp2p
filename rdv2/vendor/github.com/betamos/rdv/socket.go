package rdv

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"net/url"

	"github.com/libp2p/go-reuseport"
)

// An SO_REUSEPORT TCP socket suitable for NAT traversal/hole punching, over both ipv4 and ipv6.
// Usually, higher level abstractions should be used.
type socket struct {

	// A dual-stack (ipv4/6) TCP listener.
	//
	// TODO: Should this be refactored into two single-stack listeners, in order to support
	// non dual-stack systems? And if so, can the ports be different? See also NAT64.
	net.Listener

	// TLS config for https.
	//
	// TODO: Higher level protocols should be one layer above sockets?
	TlsConfig *tls.Config
}

func dialer(localIp netip.Addr, port uint16) *net.Dialer {
	ap := netip.AddrPortFrom(localIp, port)
	return &net.Dialer{
		Control:   reuseport.Control,
		LocalAddr: net.TCPAddrFromAddrPort(ap),
	}
}

func newSocket(ctx context.Context, port uint16, tlsConf *tls.Config) (*socket, error) {
	lc := net.ListenConfig{
		Control: reuseport.Control,
	}
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%v", port))
	if err != nil {
		return nil, err
	}
	return &socket{
		Listener:  ln,
		TlsConfig: tlsConf,
	}, nil
}

// Returns the dial- and listening port number for the socket.
func (s *socket) Port() uint16 {
	return AddrPortFrom(s.Addr()).Port()
}

// Dial an addr from a specific local addr
func (s *socket) DialAddr(ctx context.Context, laddr netip.Addr, addr netip.AddrPort) (net.Conn, error) {
	d := dialer(laddr, s.Port())
	return d.DialContext(ctx, "tcp", addr.String())
}

// For now, dials over tcp4 only. We're leveraging the OS for choosing
// a local addr (and thus interface) here.
func (s *socket) DialURL4(ctx context.Context, url *url.URL) (net.Conn, error) {
	hostPort := net.JoinHostPort(url.Hostname(), urlPort(url))
	netd := dialer(netip.IPv4Unspecified(), s.Port())
	dialFn := netd.DialContext
	if url.Scheme == "https" {
		tlsd := &tls.Dialer{
			NetDialer: netd,
			Config:    s.TlsConfig,
		}
		dialFn = tlsd.DialContext
	} else if url.Scheme != "http" {
		return nil, fmt.Errorf("unexpected scheme [%s]", url.Scheme)
	}
	// NOTE: Setting "tcp" should be enough, given the ipv4 laddr selection above. However,
	// an ipv6 addr was chosen on macOS, so we're using tcp4 here to be sure.
	return dialFn(ctx, "tcp4", hostPort)
}
