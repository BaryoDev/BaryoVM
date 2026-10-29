// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// CAA is one CAA resource record.
type CAA struct {
	Flag  uint8
	Tag   string
	Value string
}

// Resolver is the DNS seam for the edge checks. An absent record is an empty
// result with a nil error; an error means the lookup itself failed (timeout,
// server failure). Tests satisfy it with canned answers and no network.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	// LookupMX returns the MX target hosts.
	LookupMX(ctx context.Context, name string) ([]string, error)
	// LookupA returns IPv4 addresses.
	LookupA(ctx context.Context, name string) ([]string, error)
	LookupCAA(ctx context.Context, name string) ([]CAA, error)
}

// NetResolver is the real Resolver. The Go standard library has no CAA lookup,
// so CAA is a small wire-format query sent to the same servers the other
// lookups use.
type NetResolver struct {
	r       *net.Resolver
	servers func() []string // host:port list for the CAA query
}

// NewResolver returns the system resolver when pin is empty, otherwise one that
// sends every query, CAA included, to the pinned server. pin is an IP address
// with an optional port (default 53). An IP is required, not a name, so pinning
// cannot itself depend on the resolver being pinned.
func NewResolver(pin string) (*NetResolver, error) {
	if pin == "" {
		return &NetResolver{r: net.DefaultResolver, servers: systemServers}, nil
	}
	addr, err := normalizeResolver(pin)
	if err != nil {
		return nil, err
	}
	return &NetResolver{
		r: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		servers: func() []string { return []string{addr} },
	}, nil
}

func normalizeResolver(pin string) (string, error) {
	host, port, err := net.SplitHostPort(pin)
	if err != nil {
		host, port = strings.Trim(pin, "[]"), "53"
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("--resolver %q: want an IP address with an optional port, like 1.1.1.1 or 9.9.9.9:53", pin)
	}
	return net.JoinHostPort(host, port), nil
}

func systemServers() []string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseResolvConf(f)
}

func parseResolvConf(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "nameserver" {
			host := f[1]
			if i := strings.Index(host, "%"); i >= 0 { // drop an IPv6 zone
				host = host[:i]
			}
			if net.ParseIP(host) != nil {
				out = append(out, net.JoinHostPort(host, "53"))
			}
		}
	}
	return out
}

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

func (n *NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	out, err := n.r.LookupTXT(ctx, name)
	if err != nil && notFound(err) {
		return nil, nil
	}
	return out, err
}

func (n *NetResolver) LookupMX(ctx context.Context, name string) ([]string, error) {
	mx, err := n.r.LookupMX(ctx, name)
	if err != nil && len(mx) == 0 {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, m := range mx {
		out = append(out, m.Host)
	}
	return out, nil
}

func (n *NetResolver) LookupA(ctx context.Context, name string) ([]string, error) {
	ips, err := n.r.LookupIP(ctx, "ip4", name)
	if err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

func (n *NetResolver) LookupCAA(ctx context.Context, name string) ([]CAA, error) {
	servers := n.servers()
	if len(servers) == 0 {
		return nil, errors.New("no DNS server found for the CAA lookup (use --resolver)")
	}
	var last error
	for _, s := range servers {
		recs, err := queryCAA(ctx, s, name)
		if err == nil {
			return recs, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

const typeCAA = 257

// buildQuery packs a recursive-desired DNS question.
func buildQuery(id uint16, name string, qtype uint16) ([]byte, error) {
	msg := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(msg[0:], id)
	binary.BigEndian.PutUint16(msg[2:], 0x0100) // RD
	binary.BigEndian.PutUint16(msg[4:], 1)      // QDCOUNT
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if l == "" || len(l) > 63 {
			return nil, fmt.Errorf("bad DNS name %q", name)
		}
		msg = append(msg, byte(len(l))) // #nosec G115 -- label length is checked to be at most 63 above
		msg = append(msg, l...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, 1) // IN
	return msg, nil
}

func skipName(msg []byte, off int) (int, error) {
	for {
		if off >= len(msg) {
			return 0, errors.New("short DNS message")
		}
		b := msg[off]
		switch {
		case b == 0:
			return off + 1, nil
		case b&0xC0 == 0xC0:
			return off + 2, nil
		case b&0xC0 != 0:
			return 0, errors.New("bad DNS label")
		}
		off += 1 + int(b)
	}
}

// parseCAA reads a response: the CAA records in it, whether it was truncated,
// and an error for a mismatched id or a failure rcode. NXDOMAIN is not an error,
// it is "no records".
func parseCAA(id uint16, msg []byte) (recs []CAA, truncated bool, err error) {
	if len(msg) < 12 {
		return nil, false, errors.New("short DNS response")
	}
	if binary.BigEndian.Uint16(msg[0:]) != id {
		return nil, false, errors.New("DNS response id mismatch")
	}
	flags := binary.BigEndian.Uint16(msg[2:])
	if flags&0x0200 != 0 {
		return nil, true, nil
	}
	switch rcode := flags & 0xF; rcode {
	case 0:
	case 3:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("DNS server answered rcode %d", rcode)
	}
	qd := int(binary.BigEndian.Uint16(msg[4:]))
	an := int(binary.BigEndian.Uint16(msg[6:]))
	off := 12
	for i := 0; i < qd; i++ {
		if off, err = skipName(msg, off); err != nil {
			return nil, false, err
		}
		off += 4
	}
	for i := 0; i < an; i++ {
		if off, err = skipName(msg, off); err != nil {
			return nil, false, err
		}
		if off+10 > len(msg) {
			return nil, false, errors.New("short DNS answer")
		}
		typ := binary.BigEndian.Uint16(msg[off:])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
		off += 10
		if off+rdlen > len(msg) {
			return nil, false, errors.New("short DNS rdata")
		}
		rd := msg[off : off+rdlen]
		off += rdlen
		if typ != typeCAA {
			continue
		}
		if len(rd) < 2 || 2+int(rd[1]) > len(rd) {
			return nil, false, errors.New("bad CAA rdata")
		}
		tl := int(rd[1])
		recs = append(recs, CAA{Flag: rd[0], Tag: strings.ToLower(string(rd[2 : 2+tl])), Value: string(rd[2+tl:])})
	}
	return recs, false, nil
}

func queryCAA(ctx context.Context, server, name string) ([]CAA, error) {
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q, err := buildQuery(id, name, typeCAA)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, ctxOr(ctx, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Unblock a read as soon as the context ends, not at the socket deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := conn.Write(q); err != nil {
		return nil, ctxOr(ctx, err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, ctxOr(ctx, err)
	}
	recs, trunc, err := parseCAA(id, buf[:n])
	if err != nil || !trunc {
		return recs, err
	}
	return queryCAATCP(ctx, server, id, q)
}

func queryCAATCP(ctx context.Context, server string, id uint16, q []byte) ([]CAA, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, ctxOr(ctx, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	framed := binary.BigEndian.AppendUint16(nil, uint16(len(q))) // #nosec G115 -- q is a query this package built, far below 64 KiB
	if _, err := conn.Write(append(framed, q...)); err != nil {
		return nil, ctxOr(ctx, err)
	}
	var l [2]byte
	if _, err := io.ReadFull(conn, l[:]); err != nil {
		return nil, ctxOr(ctx, err)
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, ctxOr(ctx, err)
	}
	recs, _, err := parseCAA(id, resp)
	return recs, err
}

// ctxOr prefers the context's error, so a cut-off read reads as a timeout.
func ctxOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
