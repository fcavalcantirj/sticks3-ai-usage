package mdns

// service.go — what this agent publishes, and how it answers a query.
//
// Everything here is pure: a query in, a response out. No sockets, no clock,
// no interfaces. The transport half lives in responder.go. That split is
// deliberate — Go's ListenMulticastUDP turns IP_MULTICAST_LOOP off, so a
// process cannot hear its own multicast traffic and an "end to end over the
// real group" test on one machine is impossible by construction. Testing the
// protocol as a pure function is not a shortcut here, it is the only honest
// way to test it at all.

import (
	"net"
	"strings"
)

// The two names the firmware is built around. THE CONTRACT: the device calls
// MDNS.queryHost("usaged") — which resolves usaged.local. — and may also
// browse "_usaged._tcp". Change either string and every provisioned device
// stops finding this agent, with no error message anywhere.
const (
	// HostLabel is the single label of the host name, without ".local".
	HostLabel = "usaged"
	// ServiceType is the DNS-SD service type, without ".local".
	ServiceType = "_usaged._tcp"
)

// serviceEnumName is the DNS-SD "list every service type here" query that
// browsers such as `dns-sd -B _services._dns-sd._udp` send. Answering it costs
// one record and makes the agent visible to generic discovery tools, which is
// how a human confirms the responder is alive without a device.
const serviceEnumName = "_services._dns-sd._udp.local."

// TTLs, in seconds. RFC 6762 §10 recommends 120 s for records that contain a
// host name (they must follow a machine that changes address) and 75 minutes
// for the shared PTR that merely says the service exists.
const (
	hostTTL    uint32 = 120
	serviceTTL uint32 = 4500
	// legacyTTL caps what a legacy unicast resolver is told to cache. It will
	// never see our future announcements or our goodbye, so it must not hold
	// an address for two minutes (RFC 6762 §6.7).
	legacyTTL uint32 = 10
)

// TXT keys. These are PATHS, and paths only. Nothing secret ever goes in a
// TXT record: a multicast packet is the most public place on the LAN, and
// GOLDEN_RULES forbids rendering a credential anywhere. The device does not
// need these — it knows its own routes — but a human running `dns-sd -L`
// does, and they document the contract at the point of discovery.
const (
	usagePath = "/v1/usage"      // mirrors internal/api: GET /v1/usage
	pairPath  = "/v1/pair/claim" // mirrors internal/api/pairing.go: pairClaimPath
)

func defaultTXT() []string {
	return []string{"txtvers=1", "path=" + usagePath, "pair=" + pairPath}
}

// Service is the published identity of one agent: a host name, one DNS-SD
// instance, and the port the HTTP server is REALLY listening on.
type Service struct {
	Host     string // host label, e.g. "usaged" -> usaged.local.
	Instance string // instance label, e.g. "usaged-mac" -> usaged-mac._usaged._tcp.local.
	Type     string // service type, e.g. "_usaged._tcp"
	Port     uint16 // the configured listen port — never a constant
	TXT      []string
}

func (s Service) hostName() string     { return s.Host + ".local." }
func (s Service) typeName() string     { return s.Type + ".local." }
func (s Service) instanceName() string { return s.Instance + "." + s.Type + ".local." }

// --- the four records ---

// aRecord maps the host name to the address of ONE interface. The caller
// passes the address the query arrived on, which is why nothing in this
// package ever needs to guess which interface is "the" interface.
func (s Service) aRecord(ip net.IP) (record, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return record{}, false
	}
	return record{
		name:  s.hostName(),
		rtype: typeA,
		class: classIN | classFlag, // unique record: cache-flush
		ttl:   hostTTL,
		ip:    ip4,
	}, true
}

func (s Service) srvRecord() record {
	return record{
		name:   s.instanceName(),
		rtype:  typeSRV,
		class:  classIN | classFlag,
		ttl:    hostTTL,
		port:   s.Port,
		target: s.hostName(),
	}
}

func (s Service) txtRecord() record {
	return record{
		name:  s.instanceName(),
		rtype: typeTXT,
		class: classIN | classFlag,
		ttl:   hostTTL,
		txt:   s.TXT,
	}
}

// ptrRecord is SHARED, not unique — several agents may legitimately offer
// _usaged._tcp on one LAN — so it never carries the cache-flush bit.
func (s Service) ptrRecord() record {
	return record{
		name:   s.typeName(),
		rtype:  typePTR,
		class:  classIN,
		ttl:    serviceTTL,
		target: s.instanceName(),
	}
}

func (s Service) enumRecord() record {
	return record{
		name:   serviceEnumName,
		rtype:  typePTR,
		class:  classIN,
		ttl:    serviceTTL,
		target: s.typeName(),
	}
}

// all returns every record this service owns, in the order a listener most
// wants them: what exists, where it is, what it says, and its address.
func (s Service) all(ip net.IP) []record {
	out := []record{s.ptrRecord(), s.srvRecord(), s.txtRecord()}
	if a, ok := s.aRecord(ip); ok {
		out = append(out, a)
	}
	return out
}

// announcement is the unsolicited "here I am" sent on start (RFC 6762 §8.3).
func (s Service) announcement(ip net.IP) *message {
	return &message{
		flags:   flagResponse | flagAuthoritative,
		answers: s.all(ip),
	}
}

// goodbye is the same records with TTL 0 (RFC 6762 §10.1), so listeners drop
// us the moment the agent stops instead of pointing the device at a dead port
// for the next two minutes.
func (s Service) goodbye(ip net.IP) *message {
	m := s.announcement(ip)
	for i := range m.answers {
		m.answers[i].ttl = 0
	}
	return m
}

// --- answering ---

// respond builds the reply to one received query, or nil when there is nothing
// to say. ip is the address of the interface the query arrived on and is what
// the A record will carry. legacy is true when the query came from a source
// port other than 5353, which means a plain DNS resolver rather than another
// mDNS implementation. The bool result reports whether the reply must go by
// unicast rather than to the group.
func (s Service) respond(q *message, ip net.IP, legacy bool) (*message, bool) {
	// A response, or anything that is not a standard query, is not ours to
	// answer. Without this check two responders on one LAN answer each other
	// forever.
	if q == nil || q.flags&flagResponse != 0 || q.flags&opcodeMask != 0 {
		return nil, false
	}

	unicast := legacy
	var answers, additional []record

	for _, qn := range q.questions {
		if qn.class&classFlag != 0 {
			unicast = true // QU bit, RFC 6762 §5.4
		}
		if c := qn.class & classMask; c != classIN && c != classANY {
			continue
		}
		ans, add := s.answer(qn, ip)
		answers = appendUnique(answers, ans...)
		additional = appendUnique(additional, add...)
	}
	if len(answers) == 0 {
		return nil, false
	}

	// A record promoted to an answer must not also ride in the additional
	// section; some stacks treat the duplicate as a malformed message.
	var extra []record
	for _, r := range additional {
		if !containsRecord(answers, r) {
			extra = append(extra, r)
		}
	}

	m := &message{
		// RFC 6762 §18.1: the ID of a multicast response MUST be zero.
		flags:      flagResponse | flagAuthoritative,
		answers:    answers,
		additional: extra,
	}

	if legacy {
		// RFC 6762 §6.7: a legacy resolver gets its own ID and its question
		// echoed back, short TTLs, and no cache-flush bit — it is a plain DNS
		// client and knows nothing about mDNS semantics.
		m.id = q.id
		m.questions = q.questions
		makeLegacy(m.answers)
		makeLegacy(m.additional)
	}
	return m, unicast
}

// answer returns the records for a single question: what goes in the answer
// section, and what is worth sending along so the querier does not have to ask
// again.
func (s Service) answer(qn question, ip net.IP) (answers, additional []record) {
	withAddr := func(rs ...record) []record {
		if a, ok := s.aRecord(ip); ok {
			return append(rs, a)
		}
		return rs
	}

	switch strings.ToLower(qn.name) {
	case strings.ToLower(s.hostName()):
		switch qn.qtype {
		case typeA, typeANY:
			if a, ok := s.aRecord(ip); ok {
				answers = append(answers, a)
			}
		case typeAAAA:
			// Deliberately silent. This responder publishes IPv4 only, and the
			// device is IPv4. RFC 6762 §6.1 would have us send an NSEC saying
			// "no AAAA here"; silence costs the querier one timeout and keeps
			// a whole record type out of the code.
		}

	case strings.ToLower(s.typeName()):
		if qn.qtype == typePTR || qn.qtype == typeANY {
			answers = append(answers, s.ptrRecord())
			additional = withAddr(s.srvRecord(), s.txtRecord())
		}

	case strings.ToLower(s.instanceName()):
		switch qn.qtype {
		case typeSRV:
			answers = append(answers, s.srvRecord())
			additional = withAddr()
		case typeTXT:
			answers = append(answers, s.txtRecord())
		case typeANY:
			answers = append(answers, s.srvRecord(), s.txtRecord())
			additional = withAddr()
		}

	case serviceEnumName:
		if qn.qtype == typePTR || qn.qtype == typeANY {
			answers = append(answers, s.enumRecord())
		}
	}
	return answers, additional
}

// makeLegacy rewrites records for a unicast reply to a plain DNS resolver.
func makeLegacy(rs []record) {
	for i := range rs {
		rs[i].class &= classMask
		if rs[i].ttl > legacyTTL {
			rs[i].ttl = legacyTTL
		}
	}
}

// appendUnique adds records not already present, keyed by name and type. A
// query with several questions ("PTR for the type" plus "SRV for the
// instance") otherwise produces the same SRV twice.
func appendUnique(dst []record, add ...record) []record {
	for _, r := range add {
		if !containsRecord(dst, r) {
			dst = append(dst, r)
		}
	}
	return dst
}

func containsRecord(rs []record, r record) bool {
	for _, existing := range rs {
		if existing.rtype == r.rtype && strings.EqualFold(existing.name, r.name) {
			return true
		}
	}
	return false
}
