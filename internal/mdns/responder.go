package mdns

// responder.go — the transport half: multicast sockets, the read loop,
// announcements and the goodbye.
//
// Package mdns publishes this agent on the LAN so the StickS3 can find it
// without anyone typing an IP address. It answers exactly what the device
// needs — an A query for usaged.local. and a DNS-SD browse of _usaged._tcp —
// and nothing else.
//
// THREE RULES THIS FILE OBEYS.
//
//  1. NO INTERFACE NAME, NO ADDRESS, NO PORT IS EVER WRITTEN DOWN. "en0" is
//     right on one Mac and wrong on the next; the port comes from the same
//     config the HTTP server binds. Every address published below is read back
//     from the interface the query arrived on.
//
//  2. DISCOVERY IS A CONVENIENCE, NEVER A DEPENDENCY. Guest Wi-Fi filters
//     multicast, VPNs swallow it, and a Mac can be up with no IPv4 at all. Any
//     of that logs one line and leaves the agent serving normally. Start
//     therefore has no error to return.
//
//  3. THE OS RESOLVER IS NOT DISPLACED. macOS already runs mDNSResponder on
//     port 5353; the socket options Go sets for a multicast listener
//     (SO_REUSEADDR + SO_REUSEPORT) let both live on the port and each answer
//     for its own names. This one claims usaged.local. and _usaged._tcp only.

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The mDNS link-local group and port (RFC 6762 §3). IPv6 (ff02::fb) is not
// joined: the device is IPv4 and only A records are published, so a second
// address family would double the socket handling to advertise nothing new.
var groupIPv4 = net.IPv4(224, 0, 0, 251)

const (
	mdnsPort = 5353
	// maxPacket is the largest mDNS message RFC 6762 §17 allows. A short
	// buffer silently truncates a big known-answer query into a parse error.
	maxPacket = 9000
	// maxReadErrors stops a hot loop if a socket starts failing for a reason
	// that is not "we closed it", without giving up on the first hiccup.
	maxReadErrors = 10
)

// announceDelays schedules the unsolicited announcements of RFC 6762 §8.3:
// several copies, spaced out, so a device already listening learns about us
// without having to ask, and one lost packet is not the end of it.
var announceDelays = []time.Duration{0, time.Second, 2 * time.Second}

var (
	errLoopbackOnly = errors.New("listening on loopback only, nothing to advertise on the LAN")
	errNoInterface  = errors.New("no usable multicast interface with an IPv4 address")
	errNoMulticast  = errors.New("could not join the mDNS group on any interface")
)

// packetConn is the slice of *net.UDPConn this file uses. It exists so the
// read loop can be driven by an ordinary UDP socket in a test — see rule 2 in
// service.go for why a real multicast round trip cannot be tested in-process.
type packetConn interface {
	ReadFromUDP(b []byte) (int, *net.UDPAddr, error)
	WriteToUDP(b []byte, addr *net.UDPAddr) (int, error)
	Close() error
}

// ifaceConn is one joined interface: the socket, and the IPv4 address that
// queries arriving on it will be answered with.
type ifaceConn struct {
	name string
	conn packetConn
	ip   net.IP
}

// Responder publishes the service until Close is called.
type Responder struct {
	svc    Service
	logger *slog.Logger

	conns []*ifaceConn
	wg    sync.WaitGroup
	done  chan struct{}
	once  sync.Once

	// Seams. Production leaves these as the net package; tests replace them to
	// exercise the interface-selection and join-failure paths without needing a
	// particular machine to be on a particular network.
	interfaces      func() ([]net.Interface, error)
	addrsOf         func(net.Interface) ([]net.Addr, error)
	listenMulticast func(*net.Interface, *net.UDPAddr) (*net.UDPConn, error)
	hostname        func() (string, error)
}

// Start publishes this agent on the LAN and returns a handle whose Close is
// always safe to call. It never returns an error and never blocks: on any
// failure it logs one line at Info and returns a responder that does nothing,
// because the agent must serve whether or not discovery works.
func Start(listen string, logger *slog.Logger) *Responder {
	r := newResponder(logger)
	r.startOrDisable(listen)
	return r
}

func newResponder(logger *slog.Logger) *Responder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Responder{
		logger:          logger,
		done:            make(chan struct{}),
		interfaces:      net.Interfaces,
		addrsOf:         func(ifi net.Interface) ([]net.Addr, error) { return ifi.Addrs() },
		listenMulticast: joinGroup,
		hostname:        os.Hostname,
	}
}

// joinGroup opens one multicast socket on ifi. Passing the interface makes Go
// set IP_MULTICAST_IF, so replies leave by the same interface the query
// arrived on — which is why no interface name is ever written down here.
func joinGroup(ifi *net.Interface, gaddr *net.UDPAddr) (*net.UDPConn, error) {
	conn, err := net.ListenMulticastUDP("udp4", ifi, gaddr)
	if err != nil {
		return nil, err
	}
	enableMulticastLoopback(conn)
	return conn, nil
}

// startOrDisable is Start's body, split out so tests can drive it with the
// seams replaced. The single log line lives here: exactly one, never repeated
// per interface, so a Mac with six down interfaces does not produce six
// warnings about a feature nobody asked for.
func (r *Responder) startOrDisable(listen string) {
	if err := r.start(listen); err != nil {
		r.logger.Info("mdns discovery disabled", "reason", err.Error())
	}
}

// Enabled reports whether the responder is actually answering on at least one
// interface.
func (r *Responder) Enabled() bool { return len(r.conns) > 0 }

func (r *Responder) start(listen string) error {
	host, port, err := splitListen(listen)
	if err != nil {
		return err
	}
	r.svc = Service{
		Host:     HostLabel,
		Instance: r.instanceLabel(),
		Type:     ServiceType,
		Port:     port,
		TXT:      defaultTXT(),
	}

	targets, err := r.targets(host)
	if err != nil {
		return err
	}

	for _, t := range targets {
		ifi := t.ifi
		conn, err := r.listenMulticast(&ifi, &net.UDPAddr{IP: groupIPv4, Port: mdnsPort})
		if err != nil {
			// Per-interface failures are ordinary (an interface may be up with
			// no multicast route). Only the total failure below is worth a
			// line at Info.
			r.logger.Debug("mdns: join failed", "iface", ifi.Name, "err", err)
			continue
		}
		r.conns = append(r.conns, &ifaceConn{name: ifi.Name, conn: conn, ip: t.ip})
	}
	if len(r.conns) == 0 {
		return errNoMulticast
	}

	names := make([]string, 0, len(r.conns))
	for _, c := range r.conns {
		names = append(names, c.name+"="+c.ip.String())
		r.wg.Add(2)
		go r.readLoop(c)
		go r.announce(c)
	}
	r.logger.Info("mdns responder",
		"host", r.svc.hostName(),
		"service", r.svc.typeName(),
		"instance", r.svc.instanceName(),
		"port", r.svc.Port,
		"interfaces", strings.Join(names, ","))
	return nil
}

// Close sends a goodbye so listeners drop us immediately, then stops every
// goroutine. Safe on a responder that never started, and safe to call twice.
func (r *Responder) Close() error {
	r.once.Do(func() {
		if r.done != nil {
			close(r.done)
		}
		for _, c := range r.conns {
			if out, err := r.svc.goodbye(c.ip).pack(); err == nil {
				r.send(c, out, nil)
			}
		}
		for _, c := range r.conns {
			_ = c.conn.Close()
		}
		r.wg.Wait()
	})
	return nil
}

// --- interface selection ---

type target struct {
	ifi net.Interface
	ip  net.IP
}

// targets picks the interfaces to answer on and the IPv4 address to publish on
// each. When the HTTP server binds one specific address, only the interface
// holding that address is used — advertising an address the server does not
// answer on is worse than not advertising at all.
func (r *Responder) targets(host string) ([]target, error) {
	want := net.ParseIP(strings.TrimSpace(host))
	if want != nil && want.IsLoopback() {
		return nil, errLoopbackOnly
	}
	specific := want != nil && !want.IsUnspecified()

	ifs, err := r.interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	var out []target
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 ||
			ifi.Flags&net.FlagLoopback != 0 ||
			ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := r.addrsOf(ifi)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip := usableIPv4(a)
			if ip == nil {
				continue
			}
			if specific && !want.Equal(ip) {
				continue
			}
			out = append(out, target{ifi: ifi, ip: ip})
			break // one address per interface is all a device needs
		}
	}
	if len(out) == 0 {
		return nil, errNoInterface
	}
	return out, nil
}

// usableIPv4 returns the routable IPv4 address behind an interface address, or
// nil. Link-local (169.254/16) is skipped: an interface that only has one is
// not on a network where anything can reach the HTTP server.
func usableIPv4(a net.Addr) net.IP {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	default:
		return nil
	}
	ip4 := ip.To4()
	if ip4 == nil || ip4.IsUnspecified() || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
		return nil
	}
	return ip4
}

// splitListen reads the host and the REAL port out of the HTTP listen address.
// The port is never assumed: whatever the server binds is what gets published.
func splitListen(listen string) (host string, port uint16, err error) {
	h, p, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", 0, fmt.Errorf("bad listen address %q: %w", listen, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, fmt.Errorf("bad listen port %q", p)
	}
	return h, uint16(n), nil
}

// instanceLabel names this agent among possibly several on one LAN. It is
// cosmetic — the device browses the SERVICE TYPE, and resolves a fixed host
// name — so a machine name that cannot become a clean DNS label falls back to
// the plain service name rather than failing the responder.
func (r *Responder) instanceLabel() string {
	h, err := r.hostname()
	if err != nil {
		return HostLabel
	}
	// macOS reports "Name.local"; the suffix would become a second label.
	h = strings.TrimSuffix(strings.TrimSuffix(h, "."), ".local")
	suffix := sanitizeLabel(h, maxLabel-len(HostLabel)-1)
	if suffix == "" {
		return HostLabel
	}
	return HostLabel + "-" + suffix
}

// sanitizeLabel reduces an arbitrary machine name to letters, digits and
// single hyphens, truncated to max bytes. DNS-SD permits richer instance
// names, but a plain label keeps appendName simple and needs no escaping.
func sanitizeLabel(s string, max int) string {
	var b strings.Builder
	lastHyphen := true // leading hyphens are dropped
	for _, ch := range s {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			b.WriteRune(ch)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
		if b.Len() >= max {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	return out
}

// --- the loops ---

// readLoop answers queries arriving on one interface until the socket closes.
func (r *Responder) readLoop(c *ifaceConn) {
	defer r.wg.Done()

	buf := make([]byte, maxPacket)
	errCount := 0

	for {
		n, src, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.done:
				return // ordinary shutdown
			default:
			}
			errCount++
			r.logger.Debug("mdns: read failed", "iface", c.name, "err", err, "count", errCount)
			if errCount >= maxReadErrors {
				r.logger.Info("mdns: giving up on interface", "iface", c.name, "err", err.Error())
				return
			}
			continue
		}
		errCount = 0

		// Malformed packets are normal on a busy LAN and are not worth a log
		// line each; every device on the segment can put bytes in this buffer.
		q, err := unpack(buf[:n])
		if err != nil {
			continue
		}

		// A source port other than 5353 means a plain DNS resolver, not
		// another mDNS implementation (RFC 6762 §6.7).
		reply, unicast := r.svc.respond(q, c.ip, src != nil && src.Port != mdnsPort)
		if reply == nil {
			continue
		}
		out, err := reply.pack()
		if err != nil {
			r.logger.Debug("mdns: pack reply failed", "iface", c.name, "err", err)
			continue
		}
		if unicast {
			r.send(c, out, src)
		} else {
			r.send(c, out, nil)
		}
	}
}

// announce sends the unsolicited announcements, then stops. It exits early if
// the responder is closed mid-schedule.
func (r *Responder) announce(c *ifaceConn) {
	defer r.wg.Done()

	out, err := r.svc.announcement(c.ip).pack()
	if err != nil {
		r.logger.Debug("mdns: pack announcement failed", "iface", c.name, "err", err)
		return
	}
	for _, d := range announceDelays {
		if d > 0 {
			t := time.NewTimer(d)
			select {
			case <-r.done:
				t.Stop()
				return
			case <-t.C:
			}
		}
		r.send(c, out, nil)
	}
}

// send writes one packet, to dst when given and to the group otherwise. A
// failed send is never fatal — the LAN may have gone away underneath us.
func (r *Responder) send(c *ifaceConn, payload []byte, dst *net.UDPAddr) {
	if dst == nil {
		dst = &net.UDPAddr{IP: groupIPv4, Port: mdnsPort}
	}
	if _, err := c.conn.WriteToUDP(payload, dst); err != nil {
		r.logger.Debug("mdns: send failed", "iface", c.name, "dst", dst.String(), "err", err)
	}
}
