package bleprov

// creds_darwin.go — reading the Wi-Fi network and its password out of macOS.
//
// EVERY COMMAND AND EVERY EXIT CODE BELOW WAS RUN ON THIS MAC ON 2026-09-06
// (macOS 26.5, build 25F71) AND THE OUTPUT READ. The flags are not remembered
// and are not guessed; where a behaviour could not be produced on demand it is
// labelled [UNVERIFIED] at the line that handles it.
//
// THE ONE FINDING THAT SHAPES THIS FILE. On macOS 14 and later the SSID is
// PRIVACY-REDACTED for any process without Location Services authorisation:
//
//	$ networksetup -getairportnetwork en0
//	You are not associated with an AirPort network.     <- while fully associated
//	$ ipconfig getsummary en0 | grep SSID
//	  SSID : <redacted>
//
// Both were reproduced here [REAL]. So this file asks three sources in
// decreasing order of trust and reports WHICH one answered, and a
// preferred-network guess is returned with ErrSSIDRedacted rather than being
// passed off as an observation. A wrong SSID provisions a device onto a network
// that does not exist, and to the owner that is indistinguishable from broken
// hardware — so the guess is labelled and the dashboard can confirm it.
//
// THE SECOND FINDING. The Wi-Fi password is in /Library/Keychains/System.keychain
// — NOT the login keychain [REAL, read from the `keychain:` line of a real
// query] — so macOS raises an authorisation dialog, and an unanswered dialog
// blocks `security` forever [REAL: a 30 s bounded run exited 124 with no output
// and no stderr]. Every read here is therefore bounded by a context.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// The three commands. Named constants because they appear in tests, in the
// report to the owner, and in the log, and a typo in one of those is a silent
// behaviour change.
const (
	cmdNetworkSetup = "networksetup"
	cmdIPConfig     = "ipconfig"
	cmdSecurity     = "security"

	// keychainDescription and keychainService are the attributes a macOS Wi-Fi
	// password item actually carries [REAL, read back from a real item]:
	//   "desc"<blob>="AirPort network password"
	//   "svce"<blob>="AirPort"
	// Both are passed so the lookup cannot match some other item that happens
	// to share the account name.
	keychainDescription = "AirPort network password"
	keychainService     = "AirPort"

	// redactedMarker is what macOS substitutes for a value it will not show a
	// process without Location Services authorisation.
	redactedMarker = "<redacted>"

	// notAssociated is what `networksetup -getairportnetwork` prints both when
	// the interface really is not associated AND when the name is being
	// withheld — the two are indistinguishable from that command alone, which
	// is exactly why there are three sources.
	notAssociated = "You are not associated with an AirPort network."
)

// Exit codes from the `security` tool. Verified where noted.
const (
	// secItemNotFound is errSecItemNotFound. [REAL] Verified: a query for a
	// network that has never been joined exits 44 with
	// "SecKeychainSearchCopyNext: The specified item could not be found".
	secItemNotFound = 44
	// secUserCanceled is errSecUserCanceled (-128), which the tool reports as
	// 128. [UNVERIFIED] — producing it needs a human to click Deny, which
	// cannot be done from a test. The stderr match below is what actually
	// carries this case, and the code is a belt-and-braces second signal.
	secUserCanceled = 128
	// secInteractionNotAllowed is errSecInteractionNotAllowed (-25308), which
	// the tool reports as 51 [UNVERIFIED, same reason]. It is what a process
	// with no window server session gets instead of a dialog.
	secInteractionNotAllowed = 51
)

// WiFiInterface returns the BSD name of the Wi-Fi hardware port, e.g. "en0".
//
// IT IS READ, NEVER ASSUMED. "en0" is the Wi-Fi port on most Macs and is not on
// all of them; on this one `networksetup -listallhardwareports` lists eight
// ports and Wi-Fi is the fifth [REAL].
func (g *Gatherer) WiFiInterface(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.commandTimeout())
	defer cancel()

	stdout, _, err := g.runner().Run(ctx, cmdNetworkSetup, "-listallhardwareports")
	if err != nil {
		return "", errors.Join(ErrNoWiFiInterface, err)
	}
	if dev := parseWiFiDevice(string(stdout)); dev != "" {
		return dev, nil
	}
	return "", ErrNoWiFiInterface
}

// parseWiFiDevice pulls the device name out of `networksetup
// -listallhardwareports`. The output is stanzas separated by blank lines:
//
//	Hardware Port: Wi-Fi
//	Device: en0
//	Ethernet Address: c8:89:f3:c0:7e:2a
func parseWiFiDevice(out string) string {
	wanted := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Hardware Port:"); ok {
			wanted = strings.EqualFold(strings.TrimSpace(rest), "Wi-Fi")
			continue
		}
		if !wanted {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "Device:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// CurrentSSID returns the network this Mac is on, and where the answer came
// from. Three sources, in decreasing order of trust:
//
//  1. `networksetup -getairportnetwork <dev>` — the direct answer.
//  2. `ipconfig getsummary <dev>` — the interface's own association record.
//  3. `networksetup -listpreferredwirelessnetworks <dev>` — the top of the
//     preferred list, WHICH IS A GUESS.
//
// On the third it returns the name AND ErrSSIDRedacted, so a caller that
// ignores the error still gets a usable value and a caller that checks it can
// ask the owner to confirm. Sources 1 and 2 return a nil error.
func (g *Gatherer) CurrentSSID(ctx context.Context, iface string) (string, Source, error) {
	if iface == "" {
		return "", "", ErrNoWiFiInterface
	}
	cmdCtx, cancel := context.WithTimeout(ctx, g.commandTimeout())
	defer cancel()

	if out, _, err := g.runner().Run(cmdCtx, cmdNetworkSetup, "-getairportnetwork", iface); err == nil {
		if ssid, ok := parseAirportNetwork(string(out)); ok {
			return ssid, SourceNetworkSetup, nil
		}
	}

	if out, _, err := g.runner().Run(cmdCtx, cmdIPConfig, "getsummary", iface); err == nil {
		if ssid, ok := parseSummarySSID(string(out)); ok {
			return ssid, SourceIPConfig, nil
		}
	}

	// Both direct sources declined. That is either "not on Wi-Fi" or "macOS
	// will not tell this process", and NOTHING IN THE OUTPUT DISTINGUISHES
	// THEM — so fall through to the guess and let the error say so.
	out, _, err := g.runner().Run(cmdCtx, cmdNetworkSetup, "-listpreferredwirelessnetworks", iface)
	if err != nil {
		return "", "", ErrNotOnWiFi
	}
	preferred := parsePreferredNetworks(string(out))
	if len(preferred) == 0 {
		return "", "", ErrNotOnWiFi
	}
	// The preferred list is ordered most-preferred first, and macOS moves the
	// network it just joined to the top, so the head of the list is the best
	// guess available. [REAL] On this Mac the head matched the association the
	// system was actually holding.
	return preferred[0], SourcePreferred, ErrSSIDRedacted
}

// PreferredNetworks lists the networks this Mac remembers, most preferred
// first. It exists so a dashboard confirming a redacted SSID can offer a LIST
// TO CLICK rather than a box to type in — which keeps the whole feature
// zero-typing even when macOS hides the answer.
func (g *Gatherer) PreferredNetworks(ctx context.Context, iface string) ([]string, error) {
	if iface == "" {
		return nil, ErrNoWiFiInterface
	}
	ctx, cancel := context.WithTimeout(ctx, g.commandTimeout())
	defer cancel()

	out, _, err := g.runner().Run(ctx, cmdNetworkSetup, "-listpreferredwirelessnetworks", iface)
	if err != nil {
		return nil, errors.Join(ErrNotOnWiFi, err)
	}
	return parsePreferredNetworks(string(out)), nil
}

// parseAirportNetwork reads `networksetup -getairportnetwork <dev>`:
//
//	Current Wi-Fi Network: HomeNet
//
// and the two ways it declines:
//
//	You are not associated with an AirPort network.
//	Current Wi-Fi Network: <redacted>
//
// [REAL] The first decline was reproduced on this Mac while it was fully
// associated to a 5 GHz network, which is the whole reason for the fallbacks.
func parseAirportNetwork(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == notAssociated {
			continue
		}
		rest, ok := strings.CutPrefix(line, "Current Wi-Fi Network:")
		if !ok {
			// Older releases said "Current AirPort Network:".
			rest, ok = strings.CutPrefix(line, "Current AirPort Network:")
		}
		if !ok {
			continue
		}
		ssid := strings.TrimSpace(rest)
		if ssid == "" || ssid == redactedMarker {
			return "", false
		}
		return ssid, true
	}
	return "", false
}

// parseSummarySSID reads the SSID out of `ipconfig getsummary <dev>`, whose
// lines are two-space indented "key : value" pairs:
//
//	BSSID : <redacted>
//	SSID : <redacted>
//
// [REAL] That is the literal output on this Mac. The BSSID line is skipped by
// matching the key exactly rather than searching for the substring "SSID",
// which would match it.
func parseSummarySSID(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) != "SSID" {
			continue
		}
		ssid := strings.TrimSpace(value)
		if ssid == "" || ssid == redactedMarker {
			return "", false
		}
		return ssid, true
	}
	return "", false
}

// parsePreferredNetworks reads `networksetup -listpreferredwirelessnetworks
// <dev>`, which prints a header line and then one tab-indented name per line:
//
//	Preferred networks on en0:
//		IDALINA&FILIPE
//		Xiaozhi-68B9
//
// [REAL] Verified on this Mac, including that this command is NOT redacted —
// which is exactly why it is usable as the fallback.
func parsePreferredNetworks(out string) []string {
	var names []string
	for i, line := range strings.Split(out, "\n") {
		if i == 0 && strings.HasPrefix(strings.TrimSpace(line), "Preferred networks") {
			continue
		}
		// Only indented lines are names; an unindented line is a message.
		if line == "" || !(strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ")) {
			continue
		}
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Password reads the saved Wi-Fi password for ssid out of the keychain.
//
// THE VALUE RETURNED HERE IS NEVER LOGGED BY ANYTHING IN THIS PACKAGE, and this
// function does not log at all — not even a failure, because a failure message
// that quoted the command would carry the SSID into the log for no benefit.
//
// The read is INTERACTIVE: the item lives in /Library/Keychains/System.keychain
// [REAL] and macOS raises an authorisation dialog. An unanswered dialog blocks
// `security` forever [REAL, exit 124 under a 30 s bound], so the call is
// wrapped in a context and a deadline hit becomes ErrKeychainTimeout — which
// the owner reads as "you did not answer the prompt", the true statement.
func (g *Gatherer) Password(ctx context.Context, ssid string) (string, error) {
	if ssid == "" {
		return "", ErrKeychainNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, g.keychainTimeout())
	defer cancel()

	stdout, stderr, err := g.runner().Run(ctx, cmdSecurity,
		"find-generic-password", "-w",
		"-D", keychainDescription,
		"-s", keychainService,
		"-a", ssid)
	if err == nil {
		// `security -w` prints the password followed by a newline. Only the
		// trailing newline is stripped: a passphrase may legitimately end in a
		// space, and TrimSpace would silently change it into a different one.
		return strings.TrimRight(string(stdout), "\r\n"), nil
	}
	if ctx.Err() != nil {
		return "", ErrKeychainTimeout
	}
	return "", classifyKeychainError(err, string(stderr))
}

// classifyKeychainError maps what `security` reported onto the three outcomes a
// caller can do something different about. Both the exit code and the message
// are consulted: the codes are stable but only one of them could be produced on
// demand, and the messages are the signal that has actually been observed.
func classifyKeychainError(err error, stderr string) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case secItemNotFound:
			return ErrKeychainNotFound
		case secUserCanceled, secInteractionNotAllowed:
			return ErrKeychainDenied
		}
	}
	// [REAL] "The specified item could not be found in the keychain." is the
	// exact message observed with exit 44. The other two are the documented
	// SecBase messages for the codes above and are matched as a second signal.
	switch {
	case strings.Contains(stderr, "could not be found in the keychain"):
		return ErrKeychainNotFound
	case strings.Contains(stderr, "User canceled"), strings.Contains(stderr, "User interaction is not allowed"):
		return ErrKeychainDenied
	}
	return errors.Join(ErrKeychainDenied, err)
}
