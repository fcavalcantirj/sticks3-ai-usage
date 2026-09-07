package mdns

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- helpers ---

type fakePacket struct {
	payload []byte
	addr    *net.UDPAddr
}

// fakeConn stands in for a joined multicast socket. Nothing in this file
// touches the network: AGENTS.md rule 3 applies to the mDNS group as much as
// to an HTTP endpoint, and a test process that announced ai-usage.local. on the
// developer's LAN would be a real bug.
type fakeConn struct {
	mu       sync.Mutex
	writes   []fakePacket
	reads    chan fakePacket
	closed   chan struct{}
	once     sync.Once
	writeErr error
}

func newFakeConn() *fakeConn {
	return &fakeConn{reads: make(chan fakePacket, 8), closed: make(chan struct{})}
}

func (f *fakeConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	select {
	case <-f.closed:
		return 0, nil, net.ErrClosed
	case p := <-f.reads:
		n := copy(b, p.payload)
		return n, p.addr, nil
	}
}

func (f *fakeConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.writes = append(f.writes, fakePacket{payload: append([]byte(nil), b...), addr: addr})
	return len(b), nil
}

func (f *fakeConn) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeConn) sent() []fakePacket {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakePacket(nil), f.writes...)
}

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func fakeInterface(name string, flags net.Flags) net.Interface {
	return net.Interface{Index: 1, Name: name, Flags: flags}
}

func ipNet(s string) net.Addr {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

// wiredResponder returns a responder with every net seam replaced.
func wiredResponder(t *testing.T, ifs []net.Interface, addrs map[string][]net.Addr, join func(*net.Interface, *net.UDPAddr) (*net.UDPConn, error)) (*Responder, *bytes.Buffer) {
	t.Helper()
	logger, buf := testLogger()
	r := newResponder(logger)
	r.interfaces = func() ([]net.Interface, error) { return ifs, nil }
	r.addrsOf = func(ifi net.Interface) ([]net.Addr, error) { return addrs[ifi.Name], nil }
	r.listenMulticast = join
	r.hostname = func() (string, error) { return "test-mac.local", nil }
	return r, buf
}

// --- listen address parsing ---

func TestSplitListenReadsTheRealPort(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort uint16
	}{
		{"0.0.0.0:8765", "0.0.0.0", 8765},
		{"0.0.0.0:9999", "0.0.0.0", 9999},
		{"127.0.0.1:8765", "127.0.0.1", 8765},
		{":8765", "", 8765},
		{"192.168.0.42:65535", "192.168.0.42", 65535},
		{" 0.0.0.0:8080 ", "0.0.0.0", 8080},
	}
	for _, c := range cases {
		host, port, err := splitListen(c.in)
		if err != nil {
			t.Errorf("splitListen(%q): %v", c.in, err)
			continue
		}
		if host != c.wantHost || port != c.wantPort {
			t.Errorf("splitListen(%q) = %q,%d, want %q,%d", c.in, host, port, c.wantHost, c.wantPort)
		}
	}
	for _, bad := range []string{"", "8765", "0.0.0.0", "0.0.0.0:0", "0.0.0.0:70000", "0.0.0.0:http"} {
		if _, _, err := splitListen(bad); err == nil {
			t.Errorf("splitListen(%q) accepted a bad listen address", bad)
		}
	}
}

// --- interface selection ---

func TestTargetsSkipsUnusableInterfaces(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	ifs := []net.Interface{
		fakeInterface("lo0", net.FlagUp|net.FlagMulticast|net.FlagLoopback),
		fakeInterface("down0", net.FlagMulticast),
		fakeInterface("nomcast0", net.FlagUp),
		fakeInterface("v6only", up),
		fakeInterface("linklocal", up),
		fakeInterface("en5", up),
	}
	addrs := map[string][]net.Addr{
		"lo0":       {ipNet("127.0.0.1/8")},
		"down0":     {ipNet("192.168.9.9/24")},
		"nomcast0":  {ipNet("192.168.8.8/24")},
		"v6only":    {ipNet("fe80::1/64")},
		"linklocal": {ipNet("169.254.3.4/16")},
		"en5":       {ipNet("fe80::2/64"), ipNet("192.168.0.42/24")},
	}
	r, _ := wiredResponder(t, ifs, addrs, nil)

	got, err := r.targets("0.0.0.0")
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("targets = %+v, want exactly the one usable interface", got)
	}
	if got[0].ifi.Name != "en5" || !got[0].ip.Equal(net.IPv4(192, 168, 0, 42)) {
		t.Errorf("target = %s/%v, want en5/192.168.0.42", got[0].ifi.Name, got[0].ip)
	}
}

func TestTargetsHonoursASpecificListenAddress(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	ifs := []net.Interface{fakeInterface("en0", up), fakeInterface("en1", up)}
	addrs := map[string][]net.Addr{
		"en0": {ipNet("192.168.0.42/24")},
		"en1": {ipNet("10.0.0.7/24")},
	}
	r, _ := wiredResponder(t, ifs, addrs, nil)

	got, err := r.targets("10.0.0.7")
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if len(got) != 1 || got[0].ifi.Name != "en1" {
		t.Fatalf("targets = %+v, want only en1 — publishing an address the server does not bind is worse than not publishing", got)
	}

	if _, err := r.targets("172.16.0.1"); !errors.Is(err, errNoInterface) {
		t.Errorf("targets(unknown address) err = %v, want errNoInterface", err)
	}
}

func TestTargetsRefusesLoopbackOnlyListen(t *testing.T) {
	r, _ := wiredResponder(t, nil, nil, nil)
	if _, err := r.targets("127.0.0.1"); !errors.Is(err, errLoopbackOnly) {
		t.Errorf("targets(127.0.0.1) err = %v, want errLoopbackOnly", err)
	}
}

// --- graceful degradation ---

// TestJoinFailureDoesNotTakeTheAgentDown is the rule-2 test: multicast is
// filtered on plenty of networks, and none of that may stop usaged serving.
func TestJoinFailureDoesNotTakeTheAgentDown(t *testing.T) {
	ifs := []net.Interface{fakeInterface("en0", net.FlagUp|net.FlagMulticast)}
	addrs := map[string][]net.Addr{"en0": {ipNet("192.168.0.42/24")}}
	joinErr := errors.New("setsockopt: can't assign requested address")
	r, logs := wiredResponder(t, ifs, addrs, func(*net.Interface, *net.UDPAddr) (*net.UDPConn, error) {
		return nil, joinErr
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.startOrDisable("0.0.0.0:8765")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startOrDisable blocked; it must never delay the agent's startup")
	}

	if r.Enabled() {
		t.Error("responder reports enabled after every join failed")
	}
	if n := countLogs(logs, "mdns discovery disabled"); n != 1 {
		t.Errorf("disabled logged %d times, want exactly 1", n)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close on a disabled responder: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestStartIsAlwaysUsableAndNeverPanics(t *testing.T) {
	logger, logs := testLogger()
	// Loopback-only and malformed listen addresses both short-circuit before
	// any socket is opened, so this exercises the exported entry point without
	// touching the network.
	for _, listen := range []string{"127.0.0.1:8765", "[::1]:8765", "not-an-address"} {
		r := Start(listen, logger)
		if r == nil {
			t.Fatalf("Start(%q) returned nil", listen)
		}
		if r.Enabled() {
			t.Errorf("Start(%q) reports enabled", listen)
		}
		if err := r.Close(); err != nil {
			t.Errorf("Start(%q).Close(): %v", listen, err)
		}
	}
	if n := countLogs(logs, "mdns discovery disabled"); n != 3 {
		t.Errorf("disabled logged %d times, want 3 (one per failure)", n)
	}
}

func TestStartWithANilLoggerDoesNotPanic(t *testing.T) {
	r := Start("127.0.0.1:8765", nil)
	if r == nil {
		t.Fatal("Start returned nil")
	}
	_ = r.Close()
}

// --- the read loop, over real datagrams ---

func fakeStarted(t *testing.T, listen string) (*Responder, *fakeConn) {
	t.Helper()
	logger, _ := testLogger()
	r := newResponder(logger)
	r.hostname = func() (string, error) { return "test-mac.local", nil }
	_, port, err := splitListen(listen)
	if err != nil {
		t.Fatal(err)
	}
	r.svc = Service{Host: HostLabel, Instance: r.instanceLabel(), Type: ServiceType, Port: port, TXT: defaultTXT()}
	fc := newFakeConn()
	r.conns = []*ifaceConn{{name: "fake0", conn: fc, ip: net.IPv4(192, 168, 0, 42).To4()}}
	return r, fc
}

func TestReadLoopAnswersAQueryToTheGroup(t *testing.T) {
	r, fc := fakeStarted(t, "0.0.0.0:9999")
	r.wg.Add(1)
	go r.readLoop(r.conns[0])

	buf, err := query("ai-usage.local.", typeA).pack()
	if err != nil {
		t.Fatal(err)
	}
	fc.reads <- fakePacket{payload: buf, addr: &net.UDPAddr{IP: net.IPv4(192, 168, 0, 77), Port: mdnsPort}}

	pkt := waitForPacket(t, fc, 1)
	if pkt.addr.Port != mdnsPort || !pkt.addr.IP.Equal(groupIPv4) {
		t.Errorf("reply sent to %v, want the mDNS group %v:%d", pkt.addr, groupIPv4, mdnsPort)
	}
	reply, err := unpack(pkt.payload)
	if err != nil {
		t.Fatalf("unpack reply: %v", err)
	}
	a, ok := findRecord(reply.answers, "ai-usage.local.", typeA)
	if !ok {
		t.Fatalf("no A record: %+v", reply.answers)
	}
	if !a.ip.Equal(net.IPv4(192, 168, 0, 42)) {
		t.Errorf("A = %v, want the interface the query arrived on", a.ip)
	}
	_ = r.Close()
}

func TestReadLoopAnswersALegacyQueryByUnicast(t *testing.T) {
	r, fc := fakeStarted(t, "0.0.0.0:9999")
	r.wg.Add(1)
	go r.readLoop(r.conns[0])

	q := query("_ai-usage._tcp.local.", typePTR)
	q.id = 0x4242
	buf, err := q.pack()
	if err != nil {
		t.Fatal(err)
	}
	src := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 77), Port: 53535}
	fc.reads <- fakePacket{payload: buf, addr: src}

	pkt := waitForPacket(t, fc, 1)
	if pkt.addr.Port != src.Port || !pkt.addr.IP.Equal(src.IP) {
		t.Errorf("reply sent to %v, want unicast back to %v", pkt.addr, src)
	}
	reply, err := unpack(pkt.payload)
	if err != nil {
		t.Fatalf("unpack reply: %v", err)
	}
	if reply.id != 0x4242 {
		t.Errorf("id = %#x, want the query's own", reply.id)
	}
	srv, ok := findRecord(reply.additional, r.svc.instanceName(), typeSRV)
	if !ok {
		t.Fatalf("no SRV in the browse reply: %+v", reply.additional)
	}
	if srv.port != 9999 {
		t.Errorf("SRV port = %d, want the configured 9999", srv.port)
	}
	_ = r.Close()
}

func TestReadLoopIgnoresGarbage(t *testing.T) {
	r, fc := fakeStarted(t, "0.0.0.0:8765")
	r.wg.Add(1)
	go r.readLoop(r.conns[0])

	src := &net.UDPAddr{IP: net.IPv4(192, 168, 0, 77), Port: mdnsPort}
	fc.reads <- fakePacket{payload: []byte("not a dns message"), addr: src}
	fc.reads <- fakePacket{payload: []byte{}, addr: src}

	good, err := query("ai-usage.local.", typeA).pack()
	if err != nil {
		t.Fatal(err)
	}
	fc.reads <- fakePacket{payload: good, addr: src}

	// Exactly one reply: the garbage produced none, and the loop survived it.
	pkt := waitForPacket(t, fc, 1)
	if _, err := unpack(pkt.payload); err != nil {
		t.Fatalf("unpack reply: %v", err)
	}
	if n := len(fc.sent()); n != 1 {
		t.Errorf("sent %d packets, want 1", n)
	}
	_ = r.Close()
}

// --- announcements and goodbye ---

func TestAnnouncesOnStartAndSaysGoodbyeOnClose(t *testing.T) {
	restore := announceDelays
	announceDelays = []time.Duration{0, time.Millisecond, 2 * time.Millisecond}
	defer func() { announceDelays = restore }()

	r, fc := fakeStarted(t, "0.0.0.0:8765")
	r.wg.Add(1)
	go r.announce(r.conns[0])

	waitForPacket(t, fc, len(announceDelays))
	for i, pkt := range fc.sent() {
		m, err := unpack(pkt.payload)
		if err != nil {
			t.Fatalf("announcement %d: %v", i, err)
		}
		if m.flags&flagResponse == 0 || m.flags&flagAuthoritative == 0 {
			t.Errorf("announcement %d flags = %#04x, want QR and AA", i, m.flags)
		}
		if a, ok := findRecord(m.answers, "ai-usage.local.", typeA); !ok || a.ttl == 0 {
			t.Errorf("announcement %d carries no live A record", i)
		}
	}

	before := len(fc.sent())
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after := fc.sent()
	if len(after) != before+1 {
		t.Fatalf("Close sent %d packets, want exactly 1 goodbye", len(after)-before)
	}
	bye, err := unpack(after[len(after)-1].payload)
	if err != nil {
		t.Fatalf("unpack goodbye: %v", err)
	}
	if len(bye.answers) == 0 {
		t.Fatal("goodbye carries no records")
	}
	for _, rec := range bye.answers {
		if rec.ttl != 0 {
			t.Errorf("goodbye %s ttl = %d, want 0", rec.name, rec.ttl)
		}
	}
}

func TestAnnounceStopsWhenClosedMidSchedule(t *testing.T) {
	restore := announceDelays
	announceDelays = []time.Duration{0, time.Hour}
	defer func() { announceDelays = restore }()

	r, fc := fakeStarted(t, "0.0.0.0:8765")
	r.wg.Add(1)
	go r.announce(r.conns[0])
	waitForPacket(t, fc, 1)

	done := make(chan struct{})
	go func() { defer close(done); _ = r.Close() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on a pending announcement delay")
	}
}

func TestSendFailureIsNotFatal(t *testing.T) {
	r, fc := fakeStarted(t, "0.0.0.0:8765")
	fc.writeErr = errors.New("network is down")
	r.wg.Add(1)
	go r.readLoop(r.conns[0])

	buf, err := query("ai-usage.local.", typeA).pack()
	if err != nil {
		t.Fatal(err)
	}
	fc.reads <- fakePacket{payload: buf, addr: &net.UDPAddr{IP: net.IPv4(192, 168, 0, 77), Port: mdnsPort}}

	done := make(chan struct{})
	go func() { defer close(done); _ = r.Close() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a failed send wedged the responder")
	}
}

// --- instance naming ---

func TestInstanceLabelFromHostname(t *testing.T) {
	cases := map[string]string{
		"test-mac.local":            "ai-usage-test-mac",
		"Felipes MacBook Pro.local": "ai-usage-Felipes-MacBook-Pro",
		"...":                       "ai-usage",
		"":                          "ai-usage",
		strings.Repeat("z", 200):    "ai-usage-" + strings.Repeat("z", maxLabel-len(HostLabel)-1),
	}
	for host, want := range cases {
		r := newResponder(slog.Default())
		r.hostname = func() (string, error) { return host, nil }
		if got := r.instanceLabel(); got != want {
			t.Errorf("instanceLabel(%q) = %q, want %q", host, got, want)
		}
	}

	r := newResponder(slog.Default())
	r.hostname = func() (string, error) { return "", errors.New("no hostname") }
	if got := r.instanceLabel(); got != HostLabel {
		t.Errorf("instanceLabel with a failing hostname = %q, want %q", got, HostLabel)
	}
}

func TestInstanceLabelIsAlwaysAValidLabel(t *testing.T) {
	for _, host := range []string{"a.b.c", "  ", "----", "MacBook-Pro-de-Felipe.local", strings.Repeat("w", 400)} {
		r := newResponder(slog.Default())
		r.hostname = func() (string, error) { return host, nil }
		label := r.instanceLabel()
		if len(label) > maxLabel {
			t.Errorf("instanceLabel(%q) is %d bytes, over the %d-byte DNS label limit", host, len(label), maxLabel)
		}
		if _, err := appendName(nil, label+"._ai-usage._tcp.local."); err != nil {
			t.Errorf("instanceLabel(%q) = %q does not encode: %v", host, label, err)
		}
	}
}

// --- test helpers ---

func waitForPacket(t *testing.T, fc *fakeConn, want int) fakePacket {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := fc.sent(); len(got) >= want {
			return got[want-1]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for packet %d (got %d)", want, len(fc.sent()))
	return fakePacket{}
}

func countLogs(buf *bytes.Buffer, msg string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == msg {
			n++
		}
	}
	return n
}
