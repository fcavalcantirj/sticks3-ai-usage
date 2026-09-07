//go:build darwin

package main

// bleSupported is decided at COMPILE time, never by probing the adapter.
//
// The obvious-looking alternative — call Central.Enable() at startup and wire
// the Provisioner only if it succeeds — hangs the daemon. tinygo's Enable()
// waits for CoreBluetooth to report PoweredOn, and under launchd there is no
// GUI session to grant Bluetooth access, so that wait never returns: the
// process starts, logs its config, and never binds port 8765. Observed on
// 2026-09-07; the daemon sat in __psynch_cvwait with CoreBluetooth loaded and
// lsof showed no listener at all.
//
// So the radio is touched for the first time by an actual scan, in a goroutine
// the route owns, where a failure is reported to the dashboard instead of
// taking the server down with it.
const bleSupported = true
