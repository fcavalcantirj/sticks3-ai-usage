package mdns

import (
	"net"
	"strings"
	"testing"
)

var testIP = net.IPv4(192, 168, 0, 42).To4()

// serviceOn builds the service exactly as start() does, from a listen address,
// so a test can never accidentally assert against a port the wiring would not
// really publish.
func serviceOn(t *testing.T, listen string) Service {
	t.Helper()
	_, port, err := splitListen(listen)
	if err != nil {
		t.Fatalf("splitListen(%q): %v", listen, err)
	}
	return Service{Host: HostLabel, Instance: "usaged-test", Type: ServiceType, Port: port, TXT: defaultTXT()}
}

// ask packs a query, runs it through respond, and unpacks the reply, so every
// assertion below is made against bytes that really went over the wire format
// rather than against the in-memory structs.
func ask(t *testing.T, s Service, q *message, legacy bool) (*message, bool) {
	t.Helper()
	buf, err := q.pack()
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	parsed, err := unpack(buf)
	if err != nil {
		t.Fatalf("unpack query: %v", err)
	}
	reply, unicast := s.respond(parsed, testIP, legacy)
	if reply == nil {
		return nil, unicast
	}
	out, err := reply.pack()
	if err != nil {
		t.Fatalf("pack reply: %v", err)
	}
	got, err := unpack(out)
	if err != nil {
		t.Fatalf("unpack reply: %v", err)
	}
	return got, unicast
}

func query(name string, qtype uint16) *message {
	return &message{questions: []question{{name: name, qtype: qtype, class: classIN}}}
}

func findRecord(rs []record, name string, rtype uint16) (record, bool) {
	for _, r := range rs {
		if r.rtype == rtype && strings.EqualFold(r.name, name) {
			return r, true
		}
	}
	return record{}, false
}

func TestAnswersHostAQuery(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")

	reply, unicast := ask(t, s, query("usaged.local.", typeA), false)
	if reply == nil {
		t.Fatal("no reply to an A query for usaged.local.")
	}
	if unicast {
		t.Error("a query from port 5353 with no QU bit must be answered to the group")
	}
	if reply.flags&flagResponse == 0 || reply.flags&flagAuthoritative == 0 {
		t.Errorf("flags = %#04x, want QR and AA set", reply.flags)
	}
	if reply.id != 0 {
		t.Errorf("id = %d, want 0 (RFC 6762 18.1)", reply.id)
	}
	if len(reply.questions) != 0 {
		t.Errorf("a multicast response must carry no question section, got %d", len(reply.questions))
	}

	a, ok := findRecord(reply.answers, "usaged.local.", typeA)
	if !ok {
		t.Fatalf("no A record in the answer section: %+v", reply.answers)
	}
	if !a.ip.Equal(testIP) {
		t.Errorf("A = %v, want %v (the address the query arrived on)", a.ip, testIP)
	}
	if a.class&classFlag == 0 {
		t.Error("the A record must carry the cache-flush bit: it is a unique record")
	}
	if a.ttl != hostTTL {
		t.Errorf("A ttl = %d, want %d", a.ttl, hostTTL)
	}
}

func TestHostQueryIsCaseInsensitive(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	reply, _ := ask(t, s, query("USAGED.LOCAL.", typeA), false)
	if reply == nil {
		t.Fatal("DNS names are case-insensitive; an upper-case query went unanswered")
	}
}

// TestAdvertisesTheConfiguredPort is the test that stops 8765 from creeping
// back in as a constant: the SRV port must track the listen address, whatever
// it is.
func TestAdvertisesTheConfiguredPort(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:8765", "0.0.0.0:9999", "192.168.0.42:1", ":65535"} {
		_, want, err := splitListen(listen)
		if err != nil {
			t.Fatalf("splitListen(%q): %v", listen, err)
		}
		s := serviceOn(t, listen)

		reply, _ := ask(t, s, query(s.instanceName(), typeSRV), false)
		if reply == nil {
			t.Fatalf("%s: no reply to an SRV query", listen)
		}
		srv, ok := findRecord(reply.answers, s.instanceName(), typeSRV)
		if !ok {
			t.Fatalf("%s: no SRV record: %+v", listen, reply.answers)
		}
		if srv.port != want {
			t.Errorf("%s: SRV port = %d, want %d", listen, srv.port, want)
		}
		if srv.target != s.hostName() {
			t.Errorf("%s: SRV target = %q, want %q", listen, srv.target, s.hostName())
		}
		if _, ok := findRecord(reply.additional, "usaged.local.", typeA); !ok {
			t.Errorf("%s: SRV reply should carry the A record so the querier need not ask again", listen)
		}
	}
}

func TestServiceBrowseReturnsEverythingNeededToConnect(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:9999")

	reply, _ := ask(t, s, query("_usaged._tcp.local.", typePTR), false)
	if reply == nil {
		t.Fatal("no reply to a PTR browse of _usaged._tcp.local.")
	}
	ptr, ok := findRecord(reply.answers, "_usaged._tcp.local.", typePTR)
	if !ok {
		t.Fatalf("no PTR record: %+v", reply.answers)
	}
	if ptr.target != s.instanceName() {
		t.Errorf("PTR target = %q, want %q", ptr.target, s.instanceName())
	}
	if ptr.class&classFlag != 0 {
		t.Error("the PTR record is SHARED and must not carry the cache-flush bit")
	}
	if ptr.ttl != serviceTTL {
		t.Errorf("PTR ttl = %d, want %d", ptr.ttl, serviceTTL)
	}

	srv, ok := findRecord(reply.additional, s.instanceName(), typeSRV)
	if !ok {
		t.Fatalf("browse reply carries no SRV: %+v", reply.additional)
	}
	if srv.port != 9999 {
		t.Errorf("SRV port = %d, want 9999", srv.port)
	}
	if _, ok := findRecord(reply.additional, s.instanceName(), typeTXT); !ok {
		t.Error("browse reply carries no TXT")
	}
	if _, ok := findRecord(reply.additional, "usaged.local.", typeA); !ok {
		t.Error("browse reply carries no A record")
	}
}

func TestAnswersServiceTypeEnumeration(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	reply, _ := ask(t, s, query(serviceEnumName, typePTR), false)
	if reply == nil {
		t.Fatal("no reply to _services._dns-sd._udp.local.")
	}
	ptr, ok := findRecord(reply.answers, serviceEnumName, typePTR)
	if !ok || ptr.target != s.typeName() {
		t.Errorf("enumeration answer = %+v, want a PTR to %q", reply.answers, s.typeName())
	}
}

func TestTXTCarriesPathsOnly(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	reply, _ := ask(t, s, query(s.instanceName(), typeTXT), false)
	if reply == nil {
		t.Fatal("no reply to a TXT query")
	}
	txt, ok := findRecord(reply.answers, s.instanceName(), typeTXT)
	if !ok {
		t.Fatalf("no TXT record: %+v", reply.answers)
	}
	for _, kv := range txt.txt {
		key, val, found := strings.Cut(kv, "=")
		if !found {
			t.Errorf("TXT entry %q is not key=value", kv)
			continue
		}
		// Nothing that could be a credential belongs in a multicast packet.
		if key == "token" || key == "pass" || key == "password" || key == "secret" {
			t.Errorf("TXT carries a credential-shaped key %q", key)
		}
		if strings.HasPrefix(key, "path") || key == "pair" {
			if !strings.HasPrefix(val, "/") {
				t.Errorf("TXT %q = %q, want a path", key, val)
			}
		}
	}
}

func TestIgnoresResponsesAndForeignNames(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")

	resp := query("usaged.local.", typeA)
	resp.flags = flagResponse
	if reply, _ := ask(t, s, resp, false); reply != nil {
		t.Error("answered a message that was itself a response — two responders would loop forever")
	}

	op := query("usaged.local.", typeA)
	op.flags = 0x2800 // OPCODE 5 (UPDATE)
	if reply, _ := ask(t, s, op, false); reply != nil {
		t.Error("answered a non-standard opcode")
	}

	if reply, _ := ask(t, s, query("printer.local.", typeA), false); reply != nil {
		t.Error("answered a name this agent does not own")
	}
	if reply, _ := ask(t, s, query("usaged.local.", typeAAAA), false); reply != nil {
		t.Error("answered AAAA; this responder publishes IPv4 only")
	}

	wrongClass := query("usaged.local.", typeA)
	wrongClass.questions[0].class = 3 // CHAOS
	if reply, _ := ask(t, s, wrongClass, false); reply != nil {
		t.Error("answered a query in a class other than IN or ANY")
	}
}

func TestQUBitAsksForUnicast(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	q := query("usaged.local.", typeA)
	q.questions[0].class = classIN | classFlag

	reply, unicast := ask(t, s, q, false)
	if reply == nil {
		t.Fatal("no reply")
	}
	if !unicast {
		t.Error("the QU bit was set; the reply must go by unicast (RFC 6762 5.4)")
	}
	if reply.id != 0 {
		t.Error("a QU reply is still an mDNS response: id must stay 0")
	}
}

// TestLegacyResolverGetsAPlainDNSAnswer covers the `dig`-shaped case: a query
// from a source port other than 5353. It must get its own ID and question
// back, short TTLs, and no cache-flush bit, because it understands none of
// mDNS's caching rules.
func TestLegacyResolverGetsAPlainDNSAnswer(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	q := query("usaged.local.", typeA)
	q.id = 0xbeef

	reply, unicast := ask(t, s, q, true)
	if reply == nil {
		t.Fatal("no reply")
	}
	if !unicast {
		t.Error("a legacy query must be answered by unicast")
	}
	if reply.id != 0xbeef {
		t.Errorf("id = %#x, want the query's own %#x", reply.id, 0xbeef)
	}
	if len(reply.questions) != 1 || reply.questions[0].name != "usaged.local." {
		t.Errorf("questions = %+v, want the query echoed back", reply.questions)
	}
	for _, r := range append(append([]record{}, reply.answers...), reply.additional...) {
		if r.ttl > legacyTTL {
			t.Errorf("%s ttl = %d, want <= %d", r.name, r.ttl, legacyTTL)
		}
		if r.class&classFlag != 0 {
			t.Errorf("%s carries the cache-flush bit in a legacy reply", r.name)
		}
	}
}

func TestNoDuplicateRecordsAcrossSections(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	// Ask for everything at once: the A record is an answer for the first
	// question and an additional for the others.
	q := &message{questions: []question{
		{name: "usaged.local.", qtype: typeA, class: classIN},
		{name: s.typeName(), qtype: typePTR, class: classIN},
		{name: s.instanceName(), qtype: typeSRV, class: classIN},
	}}
	reply, _ := ask(t, s, q, false)
	if reply == nil {
		t.Fatal("no reply")
	}
	seen := map[string]int{}
	for _, r := range append(append([]record{}, reply.answers...), reply.additional...) {
		key := strings.ToLower(r.name) + "/" + string(rune(r.rtype))
		seen[key]++
		if seen[key] > 1 {
			t.Errorf("record %s type %d appears %d times", r.name, r.rtype, seen[key])
		}
	}
	if _, ok := findRecord(reply.answers, "usaged.local.", typeA); !ok {
		t.Error("the A record should be an ANSWER when it was asked for directly")
	}
}

func TestGoodbyeIsTheSameRecordsWithZeroTTL(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	live := s.announcement(testIP)
	bye := s.goodbye(testIP)

	if len(bye.answers) != len(live.answers) || len(bye.answers) != 4 {
		t.Fatalf("goodbye has %d records, announcement %d, want 4 each", len(bye.answers), len(live.answers))
	}
	for _, r := range bye.answers {
		if r.ttl != 0 {
			t.Errorf("%s ttl = %d, want 0 (RFC 6762 10.1)", r.name, r.ttl)
		}
	}
	// The announcement itself must still have live TTLs — goodbye must not
	// mutate the service's own records.
	for _, r := range s.announcement(testIP).answers {
		if r.ttl == 0 {
			t.Errorf("announcement record %s has ttl 0 after a goodbye was built", r.name)
		}
	}
}

func TestAnnouncementIsSelfContained(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	buf, err := s.announcement(testIP).pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	got, err := unpack(buf)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if got.flags&flagResponse == 0 || got.flags&flagAuthoritative == 0 {
		t.Errorf("flags = %#04x, want QR and AA", got.flags)
	}
	for _, want := range []struct {
		name  string
		rtype uint16
	}{
		{s.typeName(), typePTR},
		{s.instanceName(), typeSRV},
		{s.instanceName(), typeTXT},
		{s.hostName(), typeA},
	} {
		if _, ok := findRecord(got.answers, want.name, want.rtype); !ok {
			t.Errorf("announcement is missing %s type %d", want.name, want.rtype)
		}
	}
}

func TestAnnouncementWithoutAnIPv4AddressSkipsTheARecord(t *testing.T) {
	s := serviceOn(t, "0.0.0.0:8765")
	m := s.announcement(net.ParseIP("fe80::1"))
	if _, ok := findRecord(m.answers, s.hostName(), typeA); ok {
		t.Error("built an A record from a non-IPv4 address")
	}
	if len(m.answers) != 3 {
		t.Errorf("answers = %d, want 3 (PTR, SRV, TXT)", len(m.answers))
	}
}
