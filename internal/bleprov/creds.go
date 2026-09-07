package bleprov

// creds.go — where the values in a Record come from, and the rules that are the
// same on every platform.
//
// THIS IS THE HALF THAT MAKES THE FEATURE ZERO-TYPING. The Mac already knows
// the Wi-Fi network it is on, the password for it, the address and port its own
// HTTP server answers on, and how to mint a device token. Every one of those is
// gathered here without asking the owner for a single character.
//
// FOUR RULES, AND THE FIRST ONE OUTRANKS EVERYTHING ELSE IN THIS FILE:
//
//  1. NO CREDENTIAL VALUE IS EVER LOGGED. Not the Wi-Fi password, not the
//     device token. Lengths, or "set"/"unset", exactly as the netcfg and
//     pairing code already do. Credentials implements slog.LogValuer so this
//     holds even when the struct is handed to a logger by accident.
//  2. NOTHING IS HARDCODED THAT THE RUNNING SYSTEM CAN BE ASKED. Not port 8765
//     — that comes from cfg.Listen — and not the interface name "en0", which is
//     read from networksetup and can be anything.
//  3. THE OS TOOLS ARE INJECTED, NEVER CALLED DIRECTLY. Runner is
//     internal/creds.Runner reused verbatim, so the same FakeRunner that tests
//     the Keychain reader tests this too, and no test ever shells out.
//  4. A GUESS IS LABELLED AS A GUESS. See Credentials.SSIDConfident.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"usaged/internal/creds"
)

// Runner is internal/creds.Runner, reused rather than redeclared: every
// external command in this project goes through that one interface, and the
// FakeRunner and FixtureRunner beside it work here unchanged.
type Runner = creds.Runner

// Source names where a gathered value actually came from, so the dashboard can
// say "we read this" instead of implying it knows more than it does.
type Source string

const (
	// SourceNetworkSetup is `networksetup -getairportnetwork`, the direct
	// answer and the only fully trustworthy one.
	SourceNetworkSetup Source = "networksetup"
	// SourceIPConfig is `ipconfig getsummary`, which reports the SSID the
	// interface is actually associated with.
	SourceIPConfig Source = "ipconfig"
	// SourcePreferred is the top of `networksetup
	// -listpreferredwirelessnetworks`. IT IS A GUESS — see ErrSSIDRedacted.
	SourcePreferred Source = "preferred-networks"
	// SourceConfig is the agent's own configuration (the listen address).
	SourceConfig Source = "config"
	// SourceMinted is a value this agent generated (the device token).
	SourceMinted Source = "minted"
)

// Errors a caller is expected to branch on. Each one names a DIFFERENT thing
// for the owner to do, which is the only reason to have more than one.
var (
	// ErrUnsupportedOS is returned by every gathering call off macOS. There is
	// deliberately no fallback that invents values: a wrong SSID provisions a
	// device onto a network that does not exist, which looks exactly like
	// broken hardware.
	ErrUnsupportedOS = errors.New("bleprov: gathering Wi-Fi credentials is only implemented on macOS")

	// ErrNoWiFiInterface means this Mac has no Wi-Fi hardware port at all.
	ErrNoWiFiInterface = errors.New("bleprov: this Mac has no Wi-Fi interface")

	// ErrNotOnWiFi means the Wi-Fi interface exists but is not associated.
	ErrNotOnWiFi = errors.New("bleprov: this Mac is not joined to a Wi-Fi network")

	// ErrSSIDRedacted is the macOS 14+ privacy behaviour: a process without
	// Location Services authorisation is told "<redacted>" instead of the
	// network name. [REAL] Reproduced on macOS 26.5 on 2026-09-06 — both
	// `networksetup -getairportnetwork` and `ipconfig getsummary` hide it.
	// The caller may still fall back to the preferred-network guess, which is
	// why this is a distinct error and not a plain failure.
	ErrSSIDRedacted = errors.New("bleprov: macOS hid the network name from this process (Location Services)")

	// ErrKeychainNotFound means there is no saved password for that network.
	ErrKeychainNotFound = errors.New("bleprov: no saved Wi-Fi password for that network in the keychain")

	// ErrKeychainDenied means the owner refused the keychain prompt, or the
	// process was not allowed to raise one.
	ErrKeychainDenied = errors.New("bleprov: the keychain refused to release the Wi-Fi password")

	// ErrKeychainTimeout means the authorisation dialog was never answered.
	// [REAL] Verified on 2026-09-06: an unanswered prompt blocks `security`
	// indefinitely, so the read MUST be bounded by a context.
	ErrKeychainTimeout = errors.New("bleprov: the keychain prompt was not answered")

	// ErrLoopbackOnly means the agent binds 127.0.0.1 and no device can reach
	// it. The same refusal internal/mdns makes, for the same reason.
	ErrLoopbackOnly = errors.New("bleprov: the agent listens on loopback only, so the device could never reach it")

	// ErrNoLANAddress means no interface holds a routable IPv4 address.
	ErrNoLANAddress = errors.New("bleprov: no interface has a routable IPv4 address to give the device")
)

// TokenBytes is 128 bits of entropy, hex-encoded to 32 characters.
//
// THIS IS NOT A SECOND TOKEN SCHEME. It is the scheme internal/api/pairing.go
// already issues per-device tokens with — see pairTokenBytes and
// newDeviceToken() there — restated here only because those identifiers are
// unexported. See Gatherer.MintToken for why production wiring should inject
// the api package's own minter rather than rely on this one.
const TokenBytes = 16

// MintToken returns a fresh per-device token: TokenBytes from crypto/rand,
// hex-encoded so the firmware can store and send it without escaping.
//
// A TOKEN THIS FUNCTION MINTS IS NOT YET VALID. internal/api recognises a
// device token only when it is recorded in its paired-device store; a token
// that reaches the device and not that store leaves the device holding a
// credential nothing accepts. Inject Gatherer.MintToken with a function that
// mints AND records, and use this one only as the default for tests and for
// callers that record the token themselves.
func MintToken() (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("bleprov: read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// --- what a gather produces --------------------------------------------------

// Credentials is everything the Mac worked out, plus where each part came from.
// The sources and warnings exist so the dashboard can be honest: "we read your
// network name" and "we think you are on this network" are different sentences
// and the owner deserves the right one.
type Credentials struct {
	SSID       string
	SSIDSource Source
	// SSIDConfident is false when SSID is the preferred-network GUESS rather
	// than an observed association. A caller that provisions on a false here
	// should show the owner what it is about to send and let them confirm —
	// still zero typing, one extra click.
	SSIDConfident bool

	// Password is never logged, never returned by any getter that renders, and
	// is wiped by Wipe once the transfer is done.
	Password string
	// Open is true for a network with no password. It is distinct from a
	// missing password: an open network is legal and joinable.
	Open bool

	Host      string // the agent's LAN address, as the device will dial it
	Port      uint16 // the REAL listen port, read from the configuration
	Token     string // per-device token, never logged
	Interface string // e.g. "en0" — read from the system, never assumed

	// Warnings are sentences for the owner, in order. They never contain a
	// credential value: everything here is either a fixed sentence or a name
	// the access point already broadcasts.
	Warnings []string
}

// LogValue renders lengths and sources, never a value.
func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("ssid_len", len(c.SSID)),
		slog.String("ssid_source", string(c.SSIDSource)),
		slog.Bool("ssid_confident", c.SSIDConfident),
		slog.String("pass_state", setOrUnset(c.Password)),
		slog.Bool("open", c.Open),
		slog.String("host", c.Host),
		slog.Int("port", int(c.Port)),
		slog.String("token_state", setOrUnset(c.Token)),
		slog.String("iface", c.Interface),
		slog.Int("warnings", len(c.Warnings)),
	)
}

// setOrUnset is the key_state convention used across this project: say whether
// a value exists, never what it is.
func setOrUnset(v string) string {
	if v == "" {
		return "unset"
	}
	return "set"
}

// Record turns gathered credentials into the record that goes on the wire.
func (c Credentials) Record() Record {
	return Record{
		SSID:     c.SSID,
		Password: c.Password,
		Host:     c.Host,
		Port:     c.Port,
		Token:    c.Token,
	}
}

// Wipe drops the credential values. Call it once the transfer is finished, for
// the same reason the device calls Decoder::scrub(): a provisioning secret has
// no business outliving the provisioning.
func (c *Credentials) Wipe() {
	c.SSID = ""
	c.Password = ""
	c.Token = ""
	c.Host = ""
	c.Port = 0
}

// --- the gatherer ------------------------------------------------------------

// defaultKeychainTimeout bounds the keychain read. It is generous because the
// far end of it is a human reading a dialog and possibly typing an
// administrator password: the Wi-Fi password lives in
// /Library/Keychains/System.keychain, not the login keychain ([REAL], verified
// 2026-09-06), and macOS asks before releasing it.
const defaultKeychainTimeout = 90 * time.Second

// defaultCommandTimeout bounds every non-interactive probe. None of them waits
// on a person, so a slow one is a stuck one.
const defaultCommandTimeout = 10 * time.Second

// Gatherer collects everything the device needs. Zero values are usable after
// NewGatherer; every dependency is injectable so no test shells out.
type Gatherer struct {
	// Listen is the agent's configured listen address (config.Config.Listen).
	// The port is read from it and is NEVER assumed to be 8765.
	Listen string

	// Run executes the macOS tools. Defaults to a runner with a per-call
	// timeout; tests inject creds.FakeRunner.
	Run Runner

	// MintToken produces the device token. PRODUCTION WIRING SHOULD INJECT the
	// function that also records the token in internal/api's paired-device
	// store — see MintToken's own comment.
	MintToken func() (string, error)

	// KeychainTimeout bounds the interactive keychain read.
	KeychainTimeout time.Duration

	// CommandTimeout bounds every other command.
	CommandTimeout time.Duration

	Logger *slog.Logger

	// Injectable network lookups, mirroring internal/mdns.Responder so the
	// address this package hands the device and the address mDNS advertises
	// are chosen by the same rules.
	interfaces func() ([]net.Interface, error)
	addrsOf    func(net.Interface) ([]net.Addr, error)
}

// NewGatherer builds a Gatherer for the agent's real listen address.
func NewGatherer(listen string, logger *slog.Logger) *Gatherer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Gatherer{
		Listen:          listen,
		Run:             timeoutRunner{},
		MintToken:       MintToken,
		KeychainTimeout: defaultKeychainTimeout,
		CommandTimeout:  defaultCommandTimeout,
		Logger:          logger,
		interfaces:      net.Interfaces,
		addrsOf:         func(ifi net.Interface) ([]net.Addr, error) { return ifi.Addrs() },
	}
}

func (g *Gatherer) runner() Runner {
	if g.Run != nil {
		return g.Run
	}
	return timeoutRunner{}
}

func (g *Gatherer) logger() *slog.Logger {
	if g.Logger != nil {
		return g.Logger
	}
	return slog.Default()
}

func (g *Gatherer) keychainTimeout() time.Duration {
	if g.KeychainTimeout > 0 {
		return g.KeychainTimeout
	}
	return defaultKeychainTimeout
}

func (g *Gatherer) commandTimeout() time.Duration {
	if g.CommandTimeout > 0 {
		return g.CommandTimeout
	}
	return defaultCommandTimeout
}

func (g *Gatherer) mint() (string, error) {
	if g.MintToken != nil {
		return g.MintToken()
	}
	return MintToken()
}

// GatherOptions narrows what a gather does. The zero value gathers everything.
type GatherOptions struct {
	// SSID overrides the lookup. The ONLY caller that should set this is a
	// dashboard confirming a guess back to us, and the value came from a list
	// this package produced — never from free text the owner typed.
	SSID string

	// SkipPassword leaves Password empty. Used for an open network, and to
	// build a preview without raising the keychain prompt.
	SkipPassword bool

	// SkipToken leaves Token empty, which the protocol reads as "get one by
	// pairing" — a legal, normal state.
	SkipToken bool
}

// Gather collects the SSID, its password, the agent's address and port, and a
// device token, WITHOUT the owner typing anything.
//
// It returns a usable Credentials together with any warnings, and only fails
// when there is nothing worth sending: no SSID at all, or no address the device
// could reach. A missing password is NOT fatal — an open network is legal, and
// so is a device that joins and is paired afterwards — so the keychain refusing
// becomes a warning and the caller decides.
func (g *Gatherer) Gather(ctx context.Context, opts GatherOptions) (*Credentials, error) {
	c := &Credentials{}

	iface, err := g.WiFiInterface(ctx)
	if err != nil && !errors.Is(err, ErrNoWiFiInterface) {
		return nil, err
	}
	c.Interface = iface

	switch {
	case opts.SSID != "":
		c.SSID = opts.SSID
		c.SSIDSource = SourceConfig
		c.SSIDConfident = true
	case iface == "":
		return nil, ErrNoWiFiInterface
	default:
		ssid, src, ssidErr := g.CurrentSSID(ctx, iface)
		if ssidErr != nil && ssid == "" {
			return nil, ssidErr
		}
		c.SSID = ssid
		c.SSIDSource = src
		c.SSIDConfident = ssidErr == nil
		if ssidErr != nil {
			c.Warnings = append(c.Warnings,
				"macOS did not tell this agent which Wi-Fi network the Mac is on, so the most "+
					"recently preferred network was used instead — check the name before setting up the device.")
		}
	}

	if !opts.SkipPassword {
		pass, passErr := g.Password(ctx, c.SSID)
		switch {
		case passErr == nil:
			c.Password = pass
			c.Open = pass == ""
		case errors.Is(passErr, ErrKeychainNotFound):
			c.Open = true
			c.Warnings = append(c.Warnings,
				"There is no saved password for this network on this Mac, so it will be sent as an open network.")
		default:
			// Denied, timed out, or something unexpected. The transfer can
			// still go ahead — the device will simply fail to join a secured
			// network — so this is the caller's decision, not ours.
			c.Warnings = append(c.Warnings, keychainWarning(passErr))
		}
	}

	host, port, err := g.AgentAddress(iface)
	if err != nil {
		return nil, err
	}
	c.Host = host
	c.Port = port

	if !opts.SkipToken {
		tok, err := g.mint()
		if err != nil {
			return nil, err
		}
		c.Token = tok
	}

	// Lengths and states only — never a value. This is the line that would
	// leak a Wi-Fi password into the launchd log if it were written any other
	// way, which is why Credentials redacts itself.
	g.logger().Info("bleprov: gathered credentials for a device", "creds", c)
	return c, nil
}

// keychainWarning turns a keychain failure into a sentence for the owner. It
// never contains a value; the errors themselves are fixed strings.
func keychainWarning(err error) string {
	switch {
	case errors.Is(err, ErrKeychainDenied):
		return "This Mac refused to release the Wi-Fi password from the keychain, so the device " +
			"was not given one. Allow the keychain prompt and set the device up again."
	case errors.Is(err, ErrKeychainTimeout):
		return "The keychain prompt was not answered, so the device was not given a Wi-Fi password. " +
			"Set the device up again and click Allow when macOS asks."
	default:
		return "The Wi-Fi password could not be read from the keychain, so the device was not given one."
	}
}

// --- the agent's own address -------------------------------------------------

// AgentAddress returns the address and port the device should dial.
//
// THE PORT IS READ FROM THE LISTEN ADDRESS AND IS NEVER ASSUMED. THE ADDRESS IS
// THE ONE THAT ACTUALLY ROUTES AND IS NEVER AN INTERFACE NAME. The selection
// rules are internal/mdns's, deliberately: the device also resolves this agent
// over mDNS, and an A record and a BLE-provisioned host that disagree would
// send it to two different places.
//
// preferIface, when non-empty, is tried first. On a Mac that is on Ethernet and
// Wi-Fi at once, the device is going to be on the WI-FI network, so the Wi-Fi
// interface's address is the one that can actually reach the agent.
func (g *Gatherer) AgentAddress(preferIface string) (string, uint16, error) {
	host, port, err := splitListen(g.Listen)
	if err != nil {
		return "", 0, err
	}

	// A specific bind address is the answer, whatever the interfaces say:
	// advertising an address the server does not answer on is worse than not
	// advertising at all.
	if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil && !ip.IsUnspecified() {
		if ip.IsLoopback() {
			return "", 0, ErrLoopbackOnly
		}
		return ip.String(), port, nil
	}

	ifs, err := g.listInterfaces()
	if err != nil {
		return "", 0, fmt.Errorf("bleprov: list interfaces: %w", err)
	}

	var fallback string
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := g.addrsOfInterface(ifi)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip := usableIPv4(a)
			if ip == nil {
				continue
			}
			if preferIface != "" && ifi.Name == preferIface {
				return ip.String(), port, nil
			}
			if fallback == "" {
				fallback = ip.String()
			}
			break // one address per interface is all a device needs
		}
	}
	if fallback == "" {
		return "", 0, ErrNoLANAddress
	}
	return fallback, port, nil
}

func (g *Gatherer) listInterfaces() ([]net.Interface, error) {
	if g.interfaces != nil {
		return g.interfaces()
	}
	return net.Interfaces()
}

func (g *Gatherer) addrsOfInterface(ifi net.Interface) ([]net.Addr, error) {
	if g.addrsOf != nil {
		return g.addrsOf(ifi)
	}
	return ifi.Addrs()
}

// usableIPv4 returns the routable IPv4 address behind an interface address, or
// nil. Link-local (169.254/16) is skipped: an interface that only has one is
// not on a network where anything can reach the HTTP server. Copied in spirit
// and in rule from internal/mdns.usableIPv4.
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

// splitListen reads the host and the REAL port out of the HTTP listen address,
// mirroring internal/mdns.splitListen. The port is never assumed: whatever the
// server binds is what the device is told to dial.
func splitListen(listen string) (host string, port uint16, err error) {
	h, p, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", 0, fmt.Errorf("bleprov: bad listen address %q: %w", listen, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, fmt.Errorf("bleprov: bad listen port %q", p)
	}
	return h, uint16(n), nil
}

// --- running the OS tools ----------------------------------------------------

// timeoutRunner is the production Runner. Unlike creds.ExecRunner it imposes NO
// timeout of its own, because the two kinds of call here need very different
// ones: a probe answers in milliseconds, while the keychain read waits on a
// human reading a dialog. The Gatherer therefore sets the deadline on the
// context, and this runner does what it is told.
//
// [REAL] Verified 2026-09-06: `security find-generic-password -w` blocks
// INDEFINITELY on an unanswered authorisation dialog. A runner that could not
// be cancelled would wedge the daemon, so cancellation is not optional here.
type timeoutRunner struct{}

func (timeoutRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	return stdout, stderr.Bytes(), err
}
