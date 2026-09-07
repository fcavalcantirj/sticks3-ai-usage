//go:build !darwin

package main

// bleSupported is false off darwin: internal/bleprov's central is macOS-only
// (CoreBluetooth), and its stub returns ErrUnsupportedOS from every call.
// Wiring a Provisioner that can only fail is worse than wiring none — with
// none, the setup routes answer "not available in this build of usaged" and
// the dashboard explains itself rather than showing a button that always
// errors. See bleprov_darwin.go for why this is a build tag and not a probe.
const bleSupported = false
