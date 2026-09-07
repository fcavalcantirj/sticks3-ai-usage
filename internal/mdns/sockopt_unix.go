//go:build unix

package mdns

import (
	"net"
	"syscall"
)

// enableMulticastLoopback undoes one thing Go does for us. ListenMulticastUDP
// sets IP_MULTICAST_LOOP to 0 (documented in net/udpsock.go), so the sending
// host's own stack never sees the packet — and with that off, NOTHING ON THIS
// MAC CAN OBSERVE THIS RESPONDER: macOS's own mDNSResponder never receives the
// announcements, so `dns-sd -G v4 usaged.local` on the same machine reports
// nothing even when the responder is working perfectly. That is precisely the
// "green test, wrong on the wire" trap this project keeps falling into, so the
// bit is turned back on: it makes the responder verifiable from the host, and
// it lets a browser on this Mac use the same name the device does.
//
// Best effort by design. If the option cannot be set, the responder still
// answers every query from the LAN; only same-host visibility is lost, so a
// failure here must not fail the join.
func enableMulticastLoopback(conn *net.UDPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
	})
}
