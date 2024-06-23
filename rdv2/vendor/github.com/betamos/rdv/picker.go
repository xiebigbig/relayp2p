package rdv

import (
	"slices"
	"time"
)

// Picker decides which conns to use, as they come available.
// Pick is invoked when peers begin their connection attempts to each other.
// Implementations must drain the candidates channel and return all conns in
// order of preference, where conns[0] will be chosen and returned
// to the user. The channel is closed when a timeout or cancelation occurs upstream,
// or through the cancel callback.
type Picker interface {
	Pick(candidates chan *Conn, cancel func()) (conns []*Conn)
}

type connPicker struct {
	// If positive, this picker completes when the timeout expires
	timeout time.Duration

	// A hook that will cause the picker to complete immediately
	foundFn func(*Conn) bool
}

// Returns a picker which completes as soon as any conn is available.
func PickFirst() Picker {
	return connPicker{
		0,
		func(nc *Conn) bool { return true },
	}
}

// Returns a picker that completes when a p2p conn is found, or falls back to the
// relay if the timeout expires. Experimentally, it takes ~2-3 RTT to establish a p2p
// conn, whereas the relay conn is already present. Thus, "penalizing"
// the relay conn by 300-3000 ms is recommended as a balance between finding the best
// connection, while keeping establishment time reasonable.
//
// Remember to set any application-level dial/accept timeouts much higher than this
// "picking" timeout, since rdv involves several more steps, like dns lookups and
// tcp/tls establishment.
func WaitForP2P(timeout time.Duration) Picker {
	return connPicker{
		timeout,
		func(nc *Conn) bool { return !nc.IsRelay },
	}
}

// Returns a picker that always waits for a specific amount of time. This is useful
// for debugging and collecting stats.
func WaitConstant(timeout time.Duration) Picker {
	return connPicker{
		timeout,
		func(nc *Conn) bool { return false },
	}
}

func (c connPicker) Pick(candidates chan *Conn, cancel func()) (conns []*Conn) {
	if c.timeout > 0 {
		timer := time.AfterFunc(c.timeout, cancel)
		defer timer.Stop()
	}

	for nc := range candidates {
		if c.foundFn != nil && c.foundFn(nc) {
			cancel()
		}
		conns = append(conns, nc)
	}
	slices.SortStableFunc(conns, byQuality)
	return
}

// Sort function to put relays last
// Possibly use addr spaces to estimate the best quality
func byQuality(a, b *Conn) int {
	if a.IsRelay {
		return 1
	} else if b.IsRelay {
		return -1
	} else {
		return 0
	}
}
