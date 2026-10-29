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

// cleanText drops control characters (ESC, newlines, C1 controls) and caps the
// length. TXT, CAA, security.txt and header values are written by whoever runs
// the scanned domain, and a terminal obeys escape sequences in them.
func cleanText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
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
	for _, c := range []string{"100.64.0.0/10", "fc00::/7", "0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// isGlobalIP reports whether ip is a unicast address worth probing from here.
func isGlobalIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
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
