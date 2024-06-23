package rdv

import (
	"net"
	"net/netip"
	"slices"
)

// A unicast "address space" of an ip addr, for purposes of rdv connectivity.
// As a bitmask, this type can also be used as a set of addr spaces.
type AddrSpace uint32

const (

	// Denotes an invalid address space (i.e. not enumerated here)
	SpaceInvalid AddrSpace = 0

	// Public addrs are very common and useful for remote connectivity.
	// Public IPv6 addrs can also provide local connectivity.
	SpacePublic4 AddrSpace = 1 << iota
	SpacePublic6

	// Private IPv4 addrs are very common and useful for local connectivity. IPv6 local (ULA) addrs
	// are less common.
	SpacePrivate4
	SpacePrivate6

	// Link-local IPv4 addrs are not common and IPv6 addrs are not recommended due to zones.
	SpaceLink4
	SpaceLink6

	// Loopback addresses are mostly useful for testing.
	SpaceLoopback4
	SpaceLoopback6
)

const (
	// NoSpaces is the set of no spaces, which can be used to force a relay conn, disabling p2p.
	NoSpaces AddrSpace = 1 << 31

	// PublicSpaces is the set of public ipv4 and ipv6 addrs.
	PublicSpaces AddrSpace = SpacePublic4 | SpacePublic6

	// DefaultSpaces is the set of spaces suitable for p2p WAN & LAN connectivity.
	DefaultSpaces AddrSpace = SpacePublic4 | SpacePublic6 | SpacePrivate4 | SpacePrivate6

	// AllSpaces is the set of all enumerated unicast spaces.
	AllSpaces AddrSpace = ^NoSpaces
)

// Returns true if the provided addr's space is equal to this exact addr space
func (s AddrSpace) MatchesAddr(addr netip.Addr) bool {
	return s == AddrSpaceFrom(addr)
}

// Returns true if the provided space is included in this set of addr spaces
func (s AddrSpace) Includes(space AddrSpace) bool {
	return space&s != 0
}

// Returns true if the provided addr is included in this set of addr spaces
func (s AddrSpace) IncludesAddr(addr netip.Addr) bool {
	return s.Includes(AddrSpaceFrom(addr))
}

func (s AddrSpace) String() string {
	switch s {
	case SpacePublic4:
		return "public4"
	case SpacePublic6:
		return "public6"
	case SpacePrivate4:
		return "private4"
	case SpacePrivate6:
		return "private6"
	case SpaceLink4:
		return "link4"
	case SpaceLink6:
		return "link6"
	case SpaceLoopback4:
		return "loopback4"
	case SpaceLoopback6:
		return "loopback6"
	}
	return "none"
}

// Get AddrPort from a TCP- or UDP net.Addr. Returns the zero-value if not supported.
// Unmaps the ip, unlike [net.TCPAddr.AddrPort], see https://github.com/golang/go/issues/53607
func AddrPortFrom(addr net.Addr) netip.AddrPort {
	var (
		ip   net.IP
		zone string
		port int
	)
	switch addr := addr.(type) {
	case *net.TCPAddr:
		ip, zone, port = addr.IP, addr.Zone, addr.Port
	case *net.UDPAddr:
		ip, zone, port = addr.IP, addr.Zone, addr.Port
	}
	a, _ := netip.AddrFromSlice(ip)
	return netip.AddrPortFrom(a.WithZone(zone).Unmap(), uint16(port))
}

// Returns the address space of the ip address.
func AddrSpaceFrom(ip netip.Addr) AddrSpace {
	// TODO: Check what to do about ipv4-mapped ipv6 addresses
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return SpaceInvalid
	}
	if ip.IsLoopback() {
		if ip.Is4() {
			return SpaceLoopback4
		}
		return SpaceLoopback6
	}
	if ip.IsLinkLocalUnicast() {
		if ip.Is4() {
			return SpaceLink4
		}
		return SpaceLink6
	}
	if ip.IsPrivate() {
		if ip.Is4() {
			return SpacePrivate4
		}
		return SpacePrivate6
	}
	if ip.IsGlobalUnicast() {
		if ip.Is4() {
			return SpacePublic4
		}
		return SpacePublic6
	}
	return SpaceInvalid
}

// Probing addrs to use for each space.
var udpProbeAddrs = map[AddrSpace]netip.Addr{
	SpaceLoopback4: netip.MustParseAddr("127.0.0.1"),
	SpaceLink4:     netip.MustParseAddr("169.254.0.1"),
	SpacePrivate4:  netip.MustParseAddr("192.168.0.1"),
	SpacePublic4:   netip.MustParseAddr("1.1.1.1"),
	SpaceLoopback6: netip.MustParseAddr("::1"),

	// Known issue: On linux, it appears we need a valid locally defined zone to open a UDP socket.
	SpaceLink6:    netip.MustParseAddr("fe80::1"),
	SpacePrivate6: netip.MustParseAddr("fd00::1"),
	SpacePublic6:  netip.MustParseAddr("2400::1"),
}

// Returns a map of local addrs to use for each destination addr space in the set
// provided by `spaces`, through UDP probing. In rdv this helps deduplicate equivalent
// candidate addrs and ensuring that mutual dial-listen attempts occur over the same
// address tuples. This is mostly important for ipv6 which can have many
// extra "privacy addresses" per interface. The set of addrs may belong to different
// network interfaces.
//
// Note that the local- and destination addr spaces can differ. Notably, a host
// behind a home NAT reaching a public ipv4 addr typically uses a local private addr,
// like 192.168.x.x.
func probeLocalAddrs(spaces AddrSpace) map[AddrSpace]netip.Addr {
	laddrs := make(map[AddrSpace]netip.Addr)
	for space, addr := range udpProbeAddrs {
		if spaces.Includes(space) {
			laddr, _ := probeLocalAddr(addr)
			laddrs[space] = laddr
		}
	}
	return laddrs
}

// Probe the local addr we'd use for reaching the provided remote addr, through a no-op UDP socket.
// It returns the address chosen by the OS based on current routing tables,
// without having to manually retrieve and parse those on a per-platform basis, which
// is not available in the standard library.
//
// Method sourced from https://stackoverflow.com/a/37382208
func probeLocalAddr(raddr netip.Addr) (netip.Addr, error) {
	udpAddr := net.UDPAddrFromAddrPort(netip.AddrPortFrom(raddr, 53))
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()
	return AddrPortFrom(conn.LocalAddr()).Addr(), nil
}

// Returns deduplicated local addresses to share, filtered by the provided spaces.
func selfAddrs(laddrMap map[AddrSpace]netip.Addr, port uint16, spaces AddrSpace) (addrs []netip.AddrPort) {
	for _, addr := range laddrMap {
		addr = addr.WithZone("") // ipv6 link local zone is not meaningful outside of this machine
		if spaces.IncludesAddr(addr) {
			addrs = append(addrs, netip.AddrPortFrom(addr, port))
		}
	}
	slices.SortFunc(addrs, netip.AddrPort.Compare)
	return slices.Compact(addrs)
}
