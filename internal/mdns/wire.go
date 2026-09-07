package mdns

// wire.go — the DNS message format, encoded and decoded by hand.
//
// WHY BY HAND. This repository has zero external Go modules and that is not
// negotiable, so there is no dns library to reach for. The subset actually
// needed is small: one header, questions, and four record types (A, PTR, SRV,
// TXT). Everything below is RFC 1035 §4 wire format, with the two multicast
// twists from RFC 6762 that bite people — the top CLASS bit means different
// things in a question and in a record, and names may be compressed.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Record / question types (RFC 1035 §3.2.2, RFC 2782 for SRV).
const (
	typeA    uint16 = 1
	typePTR  uint16 = 12
	typeTXT  uint16 = 16
	typeAAAA uint16 = 28
	typeSRV  uint16 = 33
	typeANY  uint16 = 255
)

// CLASS values and the one bit that overloads them.
const (
	classIN  uint16 = 1
	classANY uint16 = 255

	// classFlag is the top bit of the CLASS field. In a QUESTION it is the QU
	// bit — "answer me by unicast" (RFC 6762 §5.4). In a RESOURCE RECORD it is
	// the cache-flush bit — "replace whatever you had for this name and type"
	// (§10.2). Same bit, two unrelated meanings, told apart only by which
	// section it appears in. Reading it as one thing everywhere is the classic
	// way to write a responder that half works.
	classFlag uint16 = 0x8000
	classMask uint16 = 0x7fff
)

// Header flag bits (RFC 1035 §4.1.1).
const (
	flagResponse      uint16 = 0x8000 // QR: this message is a response
	flagAuthoritative uint16 = 0x0400 // AA: we own these names
	opcodeMask        uint16 = 0x7800 // OPCODE: must be 0 (standard query)
)

const (
	headerLen       = 12
	maxName         = 255
	maxLabel        = 63
	maxTXTString    = 255
	maxPointerJumps = 16 // bounds a compression-pointer chain
)

var (
	errShortMessage = errors.New("mdns: message truncated")
	errBadName      = errors.New("mdns: malformed name")
	errNameTooLong  = errors.New("mdns: name too long")
	errBadRecord    = errors.New("mdns: malformed record")
)

// question is one entry of a message's question section. class is kept RAW,
// QU bit included, because the responder has to look at that bit.
type question struct {
	name  string // fully qualified, with the trailing dot ("usaged.local.")
	qtype uint16
	class uint16
}

// record is one resource record. Only the fields that belong to rtype carry
// meaning; rdata holds the raw payload for types this package does not model,
// so decoding never loses information it cannot represent.
type record struct {
	name  string
	rtype uint16
	class uint16 // raw, cache-flush bit included
	ttl   uint32

	ip     net.IP   // A
	target string   // PTR, SRV
	prio   uint16   // SRV
	weight uint16   // SRV
	port   uint16   // SRV
	txt    []string // TXT
	rdata  []byte   // any other type
}

// message is a whole DNS message. mDNS uses the same layout as unicast DNS;
// only the meaning of some fields differs.
type message struct {
	id         uint16
	flags      uint16
	questions  []question
	answers    []record
	authority  []record
	additional []record
}

// --- encoding ---

// appendName writes name in label form. No compression pointers are emitted:
// the messages this responder sends are a few hundred bytes at most, and a
// compressor is a large amount of fiddly code to save nothing. Decoding still
// understands pointers, because other implementations do use them.
func appendName(b []byte, name string) ([]byte, error) {
	if name == "" || name == "." {
		return append(b, 0), nil
	}
	trimmed := strings.TrimSuffix(name, ".")
	start := len(b)
	for _, label := range strings.Split(trimmed, ".") {
		if len(label) == 0 || len(label) > maxLabel {
			return nil, fmt.Errorf("%w: %q", errBadName, name)
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	if len(b)-start > maxName {
		return nil, fmt.Errorf("%w: %q", errNameTooLong, name)
	}
	return b, nil
}

// packRDATA renders the type-specific payload.
func (r record) packRDATA() ([]byte, error) {
	switch r.rtype {
	case typeA:
		ip4 := r.ip.To4()
		if ip4 == nil {
			return nil, fmt.Errorf("%w: A record needs an IPv4 address", errBadRecord)
		}
		return append([]byte(nil), ip4...), nil

	case typePTR:
		return appendName(nil, r.target)

	case typeSRV:
		b := make([]byte, 6)
		binary.BigEndian.PutUint16(b[0:], r.prio)
		binary.BigEndian.PutUint16(b[2:], r.weight)
		binary.BigEndian.PutUint16(b[4:], r.port)
		return appendName(b, r.target)

	case typeTXT:
		// RFC 6763 §6.1: a TXT record is never zero-length on the wire; an
		// "empty" one is a single zero-length string. Note the asymmetry that
		// follows: encoding no strings and decoding it back yields one empty
		// string, not none.
		if len(r.txt) == 0 {
			return []byte{0}, nil
		}
		var b []byte
		for _, s := range r.txt {
			if len(s) > maxTXTString {
				return nil, fmt.Errorf("%w: TXT string too long", errBadRecord)
			}
			b = append(b, byte(len(s)))
			b = append(b, s...)
		}
		return b, nil

	default:
		return append([]byte(nil), r.rdata...), nil
	}
}

func appendRecord(b []byte, r record) ([]byte, error) {
	b, err := appendName(b, r.name)
	if err != nil {
		return nil, err
	}
	var head [8]byte
	binary.BigEndian.PutUint16(head[0:], r.rtype)
	binary.BigEndian.PutUint16(head[2:], r.class)
	binary.BigEndian.PutUint32(head[4:], r.ttl)
	b = append(b, head[:]...)

	rdata, err := r.packRDATA()
	if err != nil {
		return nil, err
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(rdata)))
	b = append(b, length[:]...)
	return append(b, rdata...), nil
}

// pack renders the whole message. The section counts are taken from the
// slices, so they can never disagree with what is actually written.
func (m *message) pack() ([]byte, error) {
	b := make([]byte, headerLen, 512)
	binary.BigEndian.PutUint16(b[0:], m.id)
	binary.BigEndian.PutUint16(b[2:], m.flags)
	binary.BigEndian.PutUint16(b[4:], uint16(len(m.questions)))
	binary.BigEndian.PutUint16(b[6:], uint16(len(m.answers)))
	binary.BigEndian.PutUint16(b[8:], uint16(len(m.authority)))
	binary.BigEndian.PutUint16(b[10:], uint16(len(m.additional)))

	var err error
	for _, q := range m.questions {
		if b, err = appendName(b, q.name); err != nil {
			return nil, err
		}
		var tail [4]byte
		binary.BigEndian.PutUint16(tail[0:], q.qtype)
		binary.BigEndian.PutUint16(tail[2:], q.class)
		b = append(b, tail[:]...)
	}
	for _, section := range [][]record{m.answers, m.authority, m.additional} {
		for _, r := range section {
			if b, err = appendRecord(b, r); err != nil {
				return nil, err
			}
		}
	}
	return b, nil
}

// --- decoding ---

// decodeName reads a (possibly compressed) name starting at off. It returns
// the name with a trailing dot, and the offset just past the name IN THE
// RECORD — which, when a pointer was followed, is not where reading stopped.
func decodeName(msg []byte, off int) (string, int, error) {
	var sb strings.Builder
	next := -1
	jumps := 0

	for {
		if off < 0 || off >= len(msg) {
			return "", 0, errShortMessage
		}
		n := int(msg[off])

		switch {
		case n == 0:
			off++
			if next < 0 {
				next = off
			}
			if sb.Len() == 0 {
				return ".", next, nil
			}
			return sb.String(), next, nil

		case n&0xc0 == 0xc0:
			if off+1 >= len(msg) {
				return "", 0, errShortMessage
			}
			ptr := int(binary.BigEndian.Uint16(msg[off:off+2]) & 0x3fff)
			if next < 0 {
				next = off + 2
			}
			// A pointer must go strictly backwards. That single check makes a
			// compression loop impossible; the jump counter is belt and braces
			// for a chain of many tiny backward hops.
			if ptr >= off {
				return "", 0, fmt.Errorf("%w: forward compression pointer", errBadName)
			}
			jumps++
			if jumps > maxPointerJumps {
				return "", 0, fmt.Errorf("%w: compression pointer chain too long", errBadName)
			}
			off = ptr

		case n&0xc0 != 0:
			return "", 0, fmt.Errorf("%w: reserved label type", errBadName)

		default:
			if off+1+n > len(msg) {
				return "", 0, errShortMessage
			}
			sb.Write(msg[off+1 : off+1+n])
			sb.WriteByte('.')
			off += 1 + n
			if sb.Len() > maxName {
				return "", 0, errNameTooLong
			}
		}
	}
}

// unpackRDATA fills the type-specific fields of r from msg[off:off+n].
func unpackRDATA(msg []byte, r *record, off, n int) error {
	end := off + n
	if end > len(msg) {
		return errShortMessage
	}
	switch r.rtype {
	case typeA:
		if n != 4 {
			return fmt.Errorf("%w: A rdata is %d bytes", errBadRecord, n)
		}
		r.ip = net.IP(append([]byte(nil), msg[off:end]...))

	case typePTR:
		target, _, err := decodeName(msg, off)
		if err != nil {
			return err
		}
		r.target = target

	case typeSRV:
		if n < 7 {
			return fmt.Errorf("%w: SRV rdata is %d bytes", errBadRecord, n)
		}
		r.prio = binary.BigEndian.Uint16(msg[off:])
		r.weight = binary.BigEndian.Uint16(msg[off+2:])
		r.port = binary.BigEndian.Uint16(msg[off+4:])
		target, _, err := decodeName(msg, off+6)
		if err != nil {
			return err
		}
		r.target = target

	case typeTXT:
		for p := off; p < end; {
			l := int(msg[p])
			if p+1+l > end {
				return fmt.Errorf("%w: TXT string overruns rdata", errBadRecord)
			}
			r.txt = append(r.txt, string(msg[p+1:p+1+l]))
			p += 1 + l
		}

	default:
		r.rdata = append([]byte(nil), msg[off:end]...)
	}
	return nil
}

func unpackRecord(msg []byte, off int) (record, int, error) {
	var r record
	name, off, err := decodeName(msg, off)
	if err != nil {
		return r, 0, err
	}
	if off+10 > len(msg) {
		return r, 0, errShortMessage
	}
	r.name = name
	r.rtype = binary.BigEndian.Uint16(msg[off:])
	r.class = binary.BigEndian.Uint16(msg[off+2:])
	r.ttl = binary.BigEndian.Uint32(msg[off+4:])
	n := int(binary.BigEndian.Uint16(msg[off+8:]))
	off += 10
	if off+n > len(msg) {
		return r, 0, errShortMessage
	}
	if err := unpackRDATA(msg, &r, off, n); err != nil {
		return r, 0, err
	}
	return r, off + n, nil
}

// unpack parses a received datagram. Anything malformed is an error rather
// than a panic: this reads packets from every device on the LAN.
func unpack(buf []byte) (*message, error) {
	if len(buf) < headerLen {
		return nil, errShortMessage
	}
	m := &message{
		id:    binary.BigEndian.Uint16(buf[0:]),
		flags: binary.BigEndian.Uint16(buf[2:]),
	}
	counts := [4]int{
		int(binary.BigEndian.Uint16(buf[4:])),
		int(binary.BigEndian.Uint16(buf[6:])),
		int(binary.BigEndian.Uint16(buf[8:])),
		int(binary.BigEndian.Uint16(buf[10:])),
	}
	off := headerLen

	for i := 0; i < counts[0]; i++ {
		name, next, err := decodeName(buf, off)
		if err != nil {
			return nil, err
		}
		off = next
		if off+4 > len(buf) {
			return nil, errShortMessage
		}
		m.questions = append(m.questions, question{
			name:  name,
			qtype: binary.BigEndian.Uint16(buf[off:]),
			class: binary.BigEndian.Uint16(buf[off+2:]),
		})
		off += 4
	}

	sections := []*[]record{&m.answers, &m.authority, &m.additional}
	for s, dst := range sections {
		for i := 0; i < counts[s+1]; i++ {
			r, next, err := unpackRecord(buf, off)
			if err != nil {
				return nil, err
			}
			*dst = append(*dst, r)
			off = next
		}
	}
	return m, nil
}
