package mdns

import (
	"encoding/binary"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestNameRoundTrip(t *testing.T) {
	names := []string{
		"ai-usage.local.",
		"_ai-usage._tcp.local.",
		"ai-usage-mac._ai-usage._tcp.local.",
		"_services._dns-sd._udp.local.",
		".",
	}
	for _, want := range names {
		b, err := appendName(nil, want)
		if err != nil {
			t.Fatalf("appendName(%q): %v", want, err)
		}
		got, next, err := decodeName(b, 0)
		if err != nil {
			t.Fatalf("decodeName(%q): %v", want, err)
		}
		if got != want {
			t.Errorf("round trip = %q, want %q", got, want)
		}
		if next != len(b) {
			t.Errorf("next = %d, want %d", next, len(b))
		}
	}
}

func TestAppendNameRejectsBadLabels(t *testing.T) {
	cases := map[string]string{
		"empty label":    "usaged..local.",
		"label too long": strings.Repeat("x", 64) + ".local.",
		"name too long":  strings.Repeat("abcdefghij.", 30) + "local.",
	}
	for name, in := range cases {
		if _, err := appendName(nil, in); err == nil {
			t.Errorf("%s: appendName(%q) = nil error, want failure", name, in)
		}
	}
}

func TestDecodeNameFollowsCompressionPointer(t *testing.T) {
	// "local." at offset 0, then "ai-usage" + a pointer back to it.
	msg, err := appendName(nil, "local.")
	if err != nil {
		t.Fatal(err)
	}
	start := len(msg)
	msg = append(msg, byte(len("ai-usage")))
	msg = append(msg, "ai-usage"...)
	msg = append(msg, 0xc0, 0x00)

	got, next, err := decodeName(msg, start)
	if err != nil {
		t.Fatalf("decodeName: %v", err)
	}
	if got != "ai-usage.local." {
		t.Errorf("name = %q, want ai-usage.local.", got)
	}
	if next != len(msg) {
		t.Errorf("next = %d, want %d (just past the pointer)", next, len(msg))
	}
}

func TestDecodeNameRejectsSelfPointer(t *testing.T) {
	// A pointer at offset 0 aimed at offset 0: the classic decompression bomb.
	msg := []byte{0xc0, 0x00}
	if _, _, err := decodeName(msg, 0); err == nil {
		t.Fatal("decodeName accepted a self-referential pointer")
	}
}

func TestDecodeNameRejectsReservedLabelType(t *testing.T) {
	if _, _, err := decodeName([]byte{0x80, 0x00}, 0); err == nil {
		t.Fatal("decodeName accepted a reserved label type")
	}
}

// sampleMessage exercises every record type this package models, in every
// section, so the round trip covers the whole encoder.
func sampleMessage() *message {
	return &message{
		id:    0x1234,
		flags: flagResponse | flagAuthoritative,
		questions: []question{
			{name: "ai-usage.local.", qtype: typeA, class: classIN | classFlag},
			{name: "_ai-usage._tcp.local.", qtype: typePTR, class: classIN},
		},
		answers: []record{
			{name: "_ai-usage._tcp.local.", rtype: typePTR, class: classIN, ttl: 4500, target: "ai-usage-mac._ai-usage._tcp.local."},
			{name: "ai-usage.local.", rtype: typeA, class: classIN | classFlag, ttl: 120, ip: net.IPv4(192, 168, 0, 42).To4()},
		},
		authority: []record{
			{name: "ai-usage-mac._ai-usage._tcp.local.", rtype: typeTXT, class: classIN | classFlag, ttl: 120, txt: []string{"txtvers=1", "path=/v1/usage"}},
		},
		additional: []record{
			{name: "ai-usage-mac._ai-usage._tcp.local.", rtype: typeSRV, class: classIN | classFlag, ttl: 120, prio: 0, weight: 0, port: 8765, target: "ai-usage.local."},
			{name: "ai-usage.local.", rtype: 99, class: classIN, ttl: 7, rdata: []byte{1, 2, 3}},
		},
	}
}

func TestMessageRoundTrip(t *testing.T) {
	want := sampleMessage()
	buf, err := want.pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	got, err := unpack(buf)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch\n got %+v\nwant %+v", got, want)
	}
}

func TestPackWritesTheSectionCounts(t *testing.T) {
	buf, err := sampleMessage().pack()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint16{2, 2, 1, 2} {
		if got := binary.BigEndian.Uint16(buf[4+2*i:]); got != want {
			t.Errorf("count[%d] = %d, want %d", i, got, want)
		}
	}
}

// TestUnpackRejectsTruncation feeds every prefix of a valid message and
// requires an error rather than a panic — this parser reads packets from every
// device on the LAN, so a malformed one must never take the agent down.
func TestUnpackRejectsTruncation(t *testing.T) {
	buf, err := sampleMessage().pack()
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(buf); n++ {
		if _, err := unpack(buf[:n]); err == nil {
			t.Errorf("unpack(%d of %d bytes) accepted a truncated message", n, len(buf))
		}
	}
}

func TestUnpackRejectsLyingCounts(t *testing.T) {
	buf, err := (&message{flags: flagResponse}).pack()
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(buf[4:], 3) // claim three questions, send none
	if _, err := unpack(buf); err == nil {
		t.Fatal("unpack accepted a message whose counts exceed its body")
	}
}

func TestPackARecordRequiresIPv4(t *testing.T) {
	r := record{name: "ai-usage.local.", rtype: typeA, class: classIN, ttl: 120, ip: net.ParseIP("fe80::1")}
	if _, err := r.packRDATA(); err == nil {
		t.Fatal("packRDATA accepted an IPv6 address in an A record")
	}
}

func TestPackEmptyTXTIsOneZeroByte(t *testing.T) {
	r := record{name: "x.local.", rtype: typeTXT, class: classIN, ttl: 120}
	got, err := r.packRDATA()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{0}) {
		t.Errorf("empty TXT rdata = %v, want [0] (RFC 6763 6.1)", got)
	}
}
