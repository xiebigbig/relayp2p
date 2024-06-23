package rdv

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Returns an rdv http/1.1 request with the provided options
func newRdvRequest(meta *Meta, addr string, header http.Header) (*http.Request, error) {
	urlStr, err := url.JoinPath(addr, meta.Token)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(meta.Method, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if header != nil {
		req.Header = header
	}
	setUpgradeHeaders(req.Header, protocolName)
	req.Header.Set(hSelfAddrs, formatAddrPorts(meta.SelfAddrs))
	return req, nil
}

// Returns an rdv http/1.1 response with the provided options
func newRdvResponse(meta *Meta) *http.Response {
	resp := newResponse(http.StatusSwitchingProtocols)
	setUpgradeHeaders(resp.Header, protocolName)

	resp.Header.Set(hPeerAddrs, formatAddrPorts(meta.PeerAddrs))
	if meta.ObservedAddr != nil {
		resp.Header.Set(hObservedAddr, meta.ObservedAddr.String())
	}
	return resp
}

// Parses an rdv http/1.1 request. Returns errUpgrade if upgrade is missing.
func parseRdvRequest(req *http.Request) (*Meta, error) {
	// Check that upgrade is intended before protocol, to report a better error
	if err := checkUpgradeHeaders(req.Header, protocolName); err != nil {
		return nil, err
	}
	if strings.ToLower(req.Proto) != "http/1.1" {
		return nil, fmt.Errorf("%w: bad http version for upgrade %s", errUpgrade, req.Proto)
	}
	token, _ := strings.CutPrefix(req.URL.Path, "/")
	m, err := newMeta(req.Method, token)
	if err != nil {
		return nil, err
	}
	m.SelfAddrs, err = parseAddrPorts(req.Header.Get(hSelfAddrs))
	if err != nil {
		return nil, fmt.Errorf("invalid self addrs [%s]", req.Header.Get(hSelfAddrs))
	}
	if len(m.SelfAddrs) > maxAddrs-1 {
		return nil, fmt.Errorf("too many self addrs [%s]", req.Header.Get(hSelfAddrs))
	}
	return m, nil
}

// Parses an rdv http/1.1 response, and modifies to the provided meta.
func parseRdvResponse(meta *Meta, resp *http.Response) (err error) {
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("unexpected http status %v", resp.Status)
	}
	if err = checkUpgradeHeaders(resp.Header, protocolName); err != nil {
		return fmt.Errorf("%w: %v", ErrBadHandshake, err)
	}
	meta.PeerAddrs, err = parseAddrPorts(resp.Header.Get(hPeerAddrs))
	if err != nil {
		return fmt.Errorf("%w: invalid peer addrs %s", ErrBadHandshake, resp.Header.Get(hPeerAddrs))
	}
	if len(meta.PeerAddrs) > maxAddrs {
		return fmt.Errorf("%w: too many peer addrs %s", ErrBadHandshake, resp.Header.Get(hPeerAddrs))
	}

	if resp.Header.Get(hObservedAddr) != "" {
		observedAddr, err := netip.ParseAddrPort(resp.Header.Get(hObservedAddr))
		if err != nil {
			return fmt.Errorf("%w: invalid observed addr %s", ErrBadHandshake, resp.Header.Get(hObservedAddr))
		}
		meta.ObservedAddr = &observedAddr
	}
	return nil
}

// Upgrade an incoming request into a server-side rdv conn
func upgradeRdv(w http.ResponseWriter, req *http.Request) (*Conn, error) {
	meta, err := parseRdvRequest(req)
	if errors.Is(err, errUpgrade) {
		http.Error(w, err.Error(), http.StatusUpgradeRequired)
		return nil, err
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, err
	}
	nc, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		// Already checked for http version while parsing, so this should be an internal err
		http.Error(w, "", http.StatusInternalServerError)
		return nil, err
	}
	req.Body = nil
	nc.SetDeadline(time.Time{})
	sw := newRelayConn(nc, brw.Reader, meta, req)
	return sw, nil
}

// Writes a http/1.1 request and reads the response directly from the conn.
// The request's context is ignored.
func doHttp(nc net.Conn, br *bufio.Reader, req *http.Request) (*http.Response, error) {
	err := req.Write(nc)
	if err != nil {
		return nil, err
	}
	return http.ReadResponse(br, nil)
}

// Checks that "Connection: upgrade" and "Upgrade: <protocol>" is set
func checkUpgradeHeaders(h http.Header, protocol string) error {
	connection := strings.ToLower(h.Get("Connection"))
	if connection != "upgrade" {
		return fmt.Errorf("%w: requires connection upgrade", errUpgrade)
	}

	// Upgrade allows multiple comma-separated protos, but we don't, so we expect an exact match.
	upgrade := strings.TrimSpace(strings.ToLower(h.Get("Upgrade")))
	if upgrade == "" {
		return fmt.Errorf("%w: missing upgrade header", errUpgrade)
	}
	if upgrade != protocol {
		return fmt.Errorf("%w: bad upgrade %s", errUpgrade, upgrade)
	}
	return nil
}

// Set the "Connection: upgrade" and "Upgrade: <protocol>" headers
func setUpgradeHeaders(h http.Header, protocol string) {
	h.Set("Connection", "upgrade")
	h.Set("Upgrade", protocol)
}

// Slurp up a bit of the response body to aid in debugging prior to closing the response.
func slurp(resp *http.Response, size int) {
	buf := make([]byte, size)
	n, _ := io.ReadFull(resp.Body, buf)
	resp.Body = io.NopCloser(bytes.NewReader(buf[:n]))
}

// Returns an http/1.1 response for an upgraded conn
func newResponse(status int) *http.Response {
	return &http.Response{
		ProtoMajor: 1,
		ProtoMinor: 1,
		StatusCode: status,
		Header:     make(http.Header),
	}
}

// Write a response err and close the conn, with a short deadline
func writeResponseErr(nc net.Conn, statusCode int, reason string) error {
	defer nc.Close()
	resp := newResponse(statusCode)
	resp.Body = io.NopCloser(strings.NewReader(reason))

	// From HTTP std lib
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp.Header.Set("X-Content-Type-Options", "nosniff")

	nc.SetDeadline(time.Now().Add(shortWriteTimeout))
	return resp.Write(nc)
}

// Parse a comma-separated ip:port string
// TODO(https://github.com/golang/go/issues/41046): Structured http field parsing
func parseAddrPorts(addrStr string) (addrs []netip.AddrPort, err error) {
	if addrStr == "" {
		return nil, nil
	}
	parts := strings.Split(addrStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		addr, err := netip.ParseAddrPort(part)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return
}

// Returns a comma-separated ip:port string
func formatAddrPorts(addrs []netip.AddrPort) string {
	var parts []string
	for _, addr := range addrs {
		parts = append(parts, addr.String())
	}
	return strings.Join(parts, ", ")
}
