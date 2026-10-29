// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"errors"
	"net"
	"strings"
	"syscall"
	"unicode"
)

// maxTextRunes caps any string that came off the network before it is shown.
const maxTextRunes = 300

// cleanText drops control characters (ESC, newlines, C1 controls), format
// characters (bidi overrides, zero-width joiners) and line separators, and caps
// the length. TXT, CAA, security.txt and header values are written by whoever
// runs the scanned domain: a terminal obeys escape sequences in them, and a
// bidi override can make a finding read as something it is not.
func cleanText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if n == maxTextRunes {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

var nonGlobalNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001:db8::/32", "2001::/32",
		// Local-use NAT64 (RFC 8215) lets the operator pick where the IPv4
		// address sits, so there is no one place to check it. Refuse it all.
		"64:ff9b:1::/48",
	} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// global6 is the only IPv6 range assigned as global unicast. Anything outside
// it (ULA, site-local, the rest) is refused outright.
var global6 = mustCIDR("2000::/3")

// embeds4 are IPv6 prefixes that carry an IPv4 address a gateway will reach on
// our behalf. The IPv4 part is checked again, so 64:ff9b::a9fe:a9fe cannot
// stand in for 169.254.169.254.
var embeds4 = []struct {
	net *net.IPNet
	at  int
}{
	{mustCIDR("64:ff9b::/96"), 12},
	{mustCIDR("2002::/16"), 2},
}

func mustCIDR(c string) *net.IPNet {
	_, n, err := net.ParseCIDR(c)
	if err != nil {
		panic(err)
	}
	return n
}

// isGlobalIP reports whether ip is a unicast address worth probing from here.
func isGlobalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.To4() == nil {
		for _, e := range embeds4 {
			if e.net.Contains(ip) {
				return isGlobalIP(net.IP(ip[e.at : e.at+4]))
			}
		}
		if !global6.Contains(ip) {
			return false
		}
	}
	if ip.Equal(net.IPv4bcast) || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, n := range nonGlobalNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

var errNonGlobal = errors.New("refusing to connect to a non-global address")

// guardDial is a net.Dialer Control hook. It runs on the resolved address just
// before connect, so a redirect, a second resolution or a rebinding answer
// cannot reach a private address.
func guardDial(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if !isGlobalIP(net.ParseIP(host)) {
		return errNonGlobal
	}
	return nil
}
