package rdv

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
)

// Metadata associated with the rdv http handshake between client and server.
type Meta struct {
	// Request data
	Method, Token string
	SelfAddrs     []netip.AddrPort

	// Response data
	ObservedAddr *netip.AddrPort
	PeerAddrs    []netip.AddrPort
}

func newMeta(method, token string) (*Meta, error) {
	if token == "" {
		return nil, errors.New("missing rdv token")
	}
	if !(method == DIAL || method == ACCEPT) {
		return nil, fmt.Errorf("unknown rdv method [%v]", method)
	}
	return &Meta{Method: method, Token: token}, nil
}

// Returns a list of observed and self-reported addrs, de-duplicated
func (m *Meta) selfAndObservedAddrs() []netip.AddrPort {
	addrs := make([]netip.AddrPort, 0, len(m.SelfAddrs)+1)
	addrs = append(addrs, m.SelfAddrs...)

	if m.ObservedAddr != nil {
		addrs = append(addrs, *m.ObservedAddr)
	}
	slices.SortFunc(addrs, netip.AddrPort.Compare)
	return slices.Compact(addrs)
}
