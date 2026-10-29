package scan

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeResolver(t *testing.T) {
	ok := map[string]string{
		"1.1.1.1":           "1.1.1.1:53",
		"9.9.9.9:5353":      "9.9.9.9:5353",
		"2606:4700::1111":   "[2606:4700::1111]:53",
		"[2606:4700::1]:53": "[2606:4700::1]:53",
	}
	for in, want := range ok {
		got, err := normalizeResolver(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"dns.google", "dns.google:53", "1.1.1", ""} {
		if _, err := normalizeResolver(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestNewResolverSystemDefault(t *testing.T) {
	r, err := NewResolver("")
	if err != nil || r == nil {
		t.Fatalf("NewResolver(\"\") = %v, %v", r, err)
	}
	if _, err := NewResolver("not-an-ip"); err == nil {
		t.Error("a pin that is not an IP must be refused")
	}
}

func TestParseResolvConf(t *testing.T) {
	conf := "# comment\nsearch lan\nnameserver 192.168.1.1\nnameserver fe80::1%en0\nnameserver not-an-ip\noptions ndots:1\n"
	got := parseResolvConf(strings.NewReader(conf))
	want := []string{"192.168.1.1:53", "[fe80::1]:53"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// caaAnswer builds a response to query q holding the given CAA records.
func caaAnswer(q []byte, flagsExtra uint16, recs ...CAA) []byte {
	msg := append([]byte{}, q...)
	binary.BigEndian.PutUint16(msg[2:], 0x8180|flagsExtra)
	binary.BigEndian.PutUint16(msg[6:], uint16(len(recs)))
	for _, r := range recs {
		rd := append([]byte{r.Flag, byte(len(r.Tag))}, r.Tag...)
		rd = append(rd, r.Value...)
		msg = append(msg, 0xC0, 0x0C)
		msg = binary.BigEndian.AppendUint16(msg, typeCAA)
		msg = binary.BigEndian.AppendUint16(msg, 1)
		msg = binary.BigEndian.AppendUint32(msg, 60)
		msg = binary.BigEndian.AppendUint16(msg, uint16(len(rd)))
		msg = append(msg, rd...)
	}
	return msg
}

func TestBuildQueryAndParseCAA(t *testing.T) {
	q, err := buildQuery(0x1234, "example.com", typeCAA)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(q[0:]) != 0x1234 || binary.BigEndian.Uint16(q[4:]) != 1 {
		t.Errorf("bad header % x", q[:12])
	}
	if q[12] != 7 || string(q[13:20]) != "example" {
		t.Errorf("bad question % x", q[12:])
	}
	if _, err := buildQuery(1, "a..b", typeCAA); err == nil {
		t.Error("empty label accepted")
	}

	recs, trunc, err := parseCAA(0x1234, caaAnswer(q, 0,
		CAA{Tag: "issue", Value: "letsencrypt.org"}, CAA{Flag: 128, Tag: "ISSUEWILD", Value: ";"}))
	if err != nil || trunc || len(recs) != 2 {
		t.Fatalf("recs = %v, trunc %v, err %v", recs, trunc, err)
	}
	if recs[0].Tag != "issue" || recs[0].Value != "letsencrypt.org" || recs[1].Tag != "issuewild" || recs[1].Flag != 128 {
		t.Errorf("recs = %+v", recs)
	}

	if _, trunc, _ := parseCAA(0x1234, caaAnswer(q, 0x0200)); !trunc {
		t.Error("TC flag not reported")
	}
	if recs, _, err := parseCAA(0x1234, caaAnswer(q, 3)); err != nil || len(recs) != 0 {
		t.Errorf("NXDOMAIN = %v, %v, want no records and no error", recs, err)
	}
	if _, _, err := parseCAA(0x1234, caaAnswer(q, 2)); err == nil {
		t.Error("SERVFAIL must be an error")
	}
	if _, _, err := parseCAA(0x9999, caaAnswer(q, 0)); err == nil {
		t.Error("id mismatch accepted")
	}
	if _, _, err := parseCAA(0x1234, q[:5]); err == nil {
		t.Error("short message accepted")
	}
}

// fakeDNS is a UDP DNS server on loopback. It answers CAA with one issue
// record, A with 192.0.2.7, and anything else with an empty answer, and counts
// the queries it received.
type fakeDNS struct {
	conn net.PacketConn
	mu   sync.Mutex
	got  []uint16
}

func newFakeDNS(t *testing.T) *fakeDNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	f := &fakeDNS{conn: pc}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte{}, buf[:n]...)
			// Question ends at the zero label, then 4 bytes.
			end := 12
			for end < len(q) && q[end] != 0 {
				end += 1 + int(q[end])
			}
			qtype := binary.BigEndian.Uint16(q[end+1:])
			f.mu.Lock()
			f.got = append(f.got, qtype)
			f.mu.Unlock()
			q = q[:end+5]
			var resp []byte
			switch qtype {
			case typeCAA:
				resp = caaAnswer(q, 0, CAA{Tag: "issue", Value: "pinned-ca.example"})
			case 1:
				resp = append([]byte{}, q...)
				binary.BigEndian.PutUint16(resp[2:], 0x8180)
				binary.BigEndian.PutUint16(resp[6:], 1)
				resp = append(resp, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7)
			default:
				resp = append([]byte{}, q...)
				binary.BigEndian.PutUint16(resp[2:], 0x8180)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return f
}

func (f *fakeDNS) queries() []uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint16{}, f.got...)
}

func TestPinnedResolverSendsEverythingToThePinnedServer(t *testing.T) {
	srv := newFakeDNS(t)
	r, err := NewResolver(srv.conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	caa, err := r.LookupCAA(ctx, "example.test")
	if err != nil || len(caa) != 1 || caa[0].Value != "pinned-ca.example" {
		t.Fatalf("CAA = %v, %v", caa, err)
	}
	ips, err := r.LookupA(ctx, "example.test")
	if err != nil || len(ips) != 1 || ips[0] != "192.0.2.7" {
		t.Fatalf("A = %v, %v", ips, err)
	}
	var sawCAA, sawA bool
	for _, q := range srv.queries() {
		sawCAA = sawCAA || q == typeCAA
		sawA = sawA || q == 1
	}
	if !sawCAA || !sawA {
		t.Errorf("pinned server saw query types %v, want CAA and A", srv.queries())
	}
}

func TestSystemResolverIsNotPinned(t *testing.T) {
	// With no pin the CAA servers come from resolv.conf, never from a flag.
	srv := newFakeDNS(t)
	pinned, _ := NewResolver(srv.conn.LocalAddr().String())
	system, _ := NewResolver("")
	if got := pinned.servers(); len(got) != 1 || got[0] != srv.conn.LocalAddr().String() {
		t.Errorf("pinned servers = %v", got)
	}
	for _, s := range system.servers() {
		if s == srv.conn.LocalAddr().String() {
			t.Errorf("system resolver picked up the pinned address %s", s)
		}
	}
}

func TestLookupCAAWithoutServers(t *testing.T) {
	r := &NetResolver{servers: func() []string { return nil }}
	if _, err := r.LookupCAA(context.Background(), "example.com"); err == nil {
		t.Error("want an error that points at --resolver")
	}
}

func TestLookupCAATimesOutOnASilentServer(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	r := &NetResolver{servers: func() []string { return []string{pc.LocalAddr().String()} }}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = r.LookupCAA(ctx, "example.com")
	if err == nil || !isTimeout(err) {
		t.Errorf("err = %v, want a timeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("lookup outlived its context")
	}
}
