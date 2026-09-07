//go:build !unix

package mdns

import "net"

// enableMulticastLoopback is a no-op off Unix. usaged targets macOS (launchd,
// the Keychain), so the only cost on another platform is that the responder
// cannot be observed from the host it runs on; it still answers the LAN.
func enableMulticastLoopback(*net.UDPConn) {}
