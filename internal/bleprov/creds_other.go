//go:build !darwin

package bleprov

// creds_other.go — the honest answer off macOS.
//
// There is NO fallback implementation here on purpose. Everything this package
// gathers is macOS-specific (networksetup, the System keychain), and inventing
// a plausible-looking SSID would provision a device onto a network that does
// not exist — which the owner cannot tell apart from broken hardware. A clear
// refusal is the only correct behaviour, and it is what the task asked for.
//
// The wire format, the encoder and the BLE central are all portable; only the
// credential sourcing stops here.

import "context"

// WiFiInterface is not implemented off macOS.
func (g *Gatherer) WiFiInterface(context.Context) (string, error) {
	return "", ErrUnsupportedOS
}

// CurrentSSID is not implemented off macOS.
func (g *Gatherer) CurrentSSID(context.Context, string) (string, Source, error) {
	return "", "", ErrUnsupportedOS
}

// PreferredNetworks is not implemented off macOS.
func (g *Gatherer) PreferredNetworks(context.Context, string) ([]string, error) {
	return nil, ErrUnsupportedOS
}

// Password is not implemented off macOS.
func (g *Gatherer) Password(context.Context, string) (string, error) {
	return "", ErrUnsupportedOS
}
