package rdv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"time"
)

const (
	maxAddrs = 10

	shortWriteTimeout = 10 * time.Millisecond

	protocolName = "rdv/1"

	// Comma-separated list of self-reported ip:port addrs. Request only.
	hSelfAddrs = "Rdv-Self-Addrs"

	// A comma-separate list of observed and self-reported ip:port addrs of the peer. Response only.
	hPeerAddrs = "Rdv-Peer-Addrs"

	// Observed public ipv4:port addr of the requesting client, from the server's point of view.
	// Response only.
	hObservedAddr = "Rdv-Observed-Addr"

	// Commands for rdv wire protocol
	cmdContinue, cmdOther = "CONTINUE", "OTHER"

	// HTTP methods to establish rdv conns
	DIAL, ACCEPT = "DIAL", "ACCEPT"
)

var (
	// ErrBadHandshake is returned from client and server when the http upgrade to rdv failed.
	ErrBadHandshake = errors.New("bad http handshake")

	// ErrProtocol is returned upon an error in the rdv header exhange.
	ErrProtocol = errors.New("rdv protocol error")

	// An error in the http upgrade
	errUpgrade = errors.New("invalid rdv upgrade")
)

// ErrOther indicates that a p2p conn was established directly between peers.
// This is the intended outcome, but considered an error server side, which expects to relay data.
type ErrOther struct {
	// The peer remote addr reported by the dialing client
	Addr netip.AddrPort
}

func (e ErrOther) Error() string {
	return fmt.Sprintf("rdv other: %v", e.Addr)
}

// An rdv header, exchanged between peers, e.g. "rdv/1 DIAL token"
type header struct {
	method, token string
}

// Reads a LF-suffixed line from a [bufio.Reader], including the LF
func readLine(br *bufio.Reader) (string, error) {
	// ReadSlice is used over ReadString/Bytes because it's limited by buffer size.
	p, err := br.ReadSlice('\n')
	if len(p) > 0 && err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return string(p), err
}

// Reads and parses the rdv header line
func readHeader(br *bufio.Reader) (*header, error) {
	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	var hdr header
	var protoName, token string
	_, err = fmt.Sscanf(line, "%v %s %s\n", &protoName, &hdr.method, &token)
	if err != nil || protoName != protocolName {
		return nil, fmt.Errorf("%w: malformed header", ErrProtocol)
	}
	if hdr.token, err = url.PathUnescape(token); err != nil {
		return nil, fmt.Errorf("%w: malformed header token", ErrProtocol)
	}
	return &hdr, nil
}

// Write an rdv header line
func writeHeader(w io.Writer, h header) error {
	_, err := fmt.Fprintf(w, "%v %s %s\n", protocolName, h.method, url.PathEscape(h.token))
	return err
}

// Reads a command line, and returns nil if CONTINUE, an io error or [ErrOther]
func readCmdContinue(br *bufio.Reader) error {
	line, err := readLine(br)
	if err != nil {
		return err
	}
	var cmd, arg string
	_, err = fmt.Sscanf(line, "%v %s\n", &cmd, &arg)
	if cmd == cmdContinue {
		return nil
	}
	if err != nil {
		return err
	}
	if cmd == cmdOther {
		addr, _ := netip.ParseAddrPort(arg)
		return ErrOther{addr}
	}
	return fmt.Errorf("%w: invalid command", ErrProtocol)
}

// Write the CONTINUE command
func writeCmdContinue(w io.Writer) error {
	_, err := fmt.Fprintf(w, "%s\n", cmdContinue)
	return err
}

// Write the OTHER <ip:port> command
func writeCmdOther(w io.Writer, addr netip.AddrPort) error {
	_, err := fmt.Fprintf(w, "%s %s\n", cmdOther, addr)
	return err
}
