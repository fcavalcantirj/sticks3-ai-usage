//go:build darwin

package bleprov

// central.go — the radio half: find a StickS3, pair with it, and hand it
// everything the Mac knows.
//
// macOS supports the CENTRAL role only — it can scan, connect, write and
// subscribe, but it cannot advertise or host a GATT server. That is exactly the
// topology this protocol needs: the device advertises, the Mac connects. The
// file is therefore darwin-only, and central_other.go answers ErrUnsupportedOS
// everywhere else rather than pretending.
//
// EVERYTHING BYTE-SHAPED LIVES IN wire.go AND IS TESTED THERE WITH NO RADIO.
// This file owns only the conversation: connect, read Info, subscribe (which is
// what starts pairing), write BEGIN, write chunks, write COMMIT, wait for
// Applied. Nothing here builds or parses a stream by hand.
//
// TWO THINGS THAT WILL BITE A READER OF THE LIBRARY:
//
//  1. DeviceCharacteristic.GetMTU() IS NOT ATT_MTU. On darwin it returns
//     CoreBluetooth's maximumWriteValueLengthForType:WithoutResponse, which is
//     ATT_MTU - 3 — the number of VALUE bytes one un-fragmented write can
//     carry. ChunkSize() takes exactly that number, which is why it subtracts
//     only the sequence byte and not the ATT header twice.
//  2. EnableNotifications and Write both give up after a hardcoded 10 s inside
//     the library. Subscribing is what triggers the pairing prompt, and a human
//     reading six digits off a 1.14" screen takes longer than that, so the
//     subscribe is RETRIED until PairTimeout. [UNVERIFIED] — the retry has not
//     been exercised against a real pairing dialog; see docs/BLE_PROVISIONING.md
//     section 9, question 1.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// The parsed UUIDs. Parsed once at init from the strings in wire.go so a typo
// is a startup panic in a test rather than a device that is never found.
var (
	serviceUUID = mustParseUUID(ServiceUUIDString)
	infoUUID    = mustParseUUID(InfoUUIDString)
	controlUUID = mustParseUUID(ControlUUIDString)
	dataUUID    = mustParseUUID(DataUUIDString)
	statusUUID  = mustParseUUID(StatusUUIDString)
)

func mustParseUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic("bleprov: bad UUID constant " + s + ": " + err.Error())
	}
	return u
}

// Radio-level failures a caller branches on.
var (
	// ErrAdapterUnavailable means Bluetooth is off, or this process has not
	// been granted Bluetooth permission.
	ErrAdapterUnavailable = errors.New("bleprov: the Bluetooth adapter is unavailable — check that Bluetooth is on")

	// ErrNoDevice means nothing advertising the provisioning service was seen.
	ErrNoDevice = errors.New("bleprov: no StickS3 advertising the setup service was found")

	// ErrBondLost is an "insufficient authentication" refusal on a write: the
	// device was factory-reset, or this Mac's bond store was cleared. The
	// document's instruction is to drop the bond and re-pair, which on macOS
	// means the owner removes the device in System Settings > Bluetooth.
	ErrBondLost = errors.New("bleprov: the device no longer recognises this Mac — forget it in System Settings > Bluetooth and set it up again")

	// ErrPairingTimeout means the passkey was never entered.
	ErrPairingTimeout = errors.New("bleprov: pairing was not completed — the six digits on the device screen were not entered")

	// ErrDeviceVersion means the device speaks a format this agent does not.
	ErrDeviceVersion = errors.New("bleprov: this device's firmware speaks a different provisioning format — update it")

	// ErrBusy means another provisioning run holds the radio. One at a time:
	// the device refuses a second central while a transfer is in flight, and
	// so does this.
	ErrBusy = errors.New("bleprov: another device setup is already in progress")
)

// --- what a scan finds -------------------------------------------------------

// Peripheral is one device seen advertising the provisioning service.
//
// Address is an OPAQUE HANDLE, not a MAC: CoreBluetooth randomises peripheral
// identifiers per host, so this string is meaningful only to this Mac and only
// until the device is forgotten. The real MAC arrives in Info once connected,
// and that is what the dashboard should show the owner.
type Peripheral struct {
	Address string    `json:"address"`
	Name    string    `json:"name"`
	RSSI    int16     `json:"rssi"`
	Seen    time.Time `json:"seen"`
}

// --- the central -------------------------------------------------------------

const (
	defaultScanWindow   = 6 * time.Second
	defaultPairTimeout  = 3 * time.Minute
	defaultApplyTimeout = 60 * time.Second

	// enableRetryInterval paces the subscribe retries that wait out the
	// pairing dialog. Each attempt costs the library's own 10 s timeout, so
	// this only spaces the failures apart.
	enableRetryInterval = 500 * time.Millisecond
)

// Central owns the Bluetooth adapter. One per process: CoreBluetooth's manager
// is process-global, and a second Central would fight the first over the same
// scan and the same connections.
type Central struct {
	// PairTimeout bounds the wait on the owner typing the passkey.
	PairTimeout time.Duration
	// ApplyTimeout bounds the wait between COMMIT and Applied, which covers a
	// real Wi-Fi join.
	ApplyTimeout time.Duration

	logger  *slog.Logger
	adapter *bluetooth.Adapter

	mu      sync.Mutex // serialises Enable, Scan and Provision: one radio
	enabled bool
}

// NewCentral builds a Central over the default adapter. It does NOT touch the
// radio — Enable does — so constructing one in New() costs nothing on a Mac
// with Bluetooth turned off.
func NewCentral(logger *slog.Logger) *Central {
	if logger == nil {
		logger = slog.Default()
	}
	return &Central{
		PairTimeout:  defaultPairTimeout,
		ApplyTimeout: defaultApplyTimeout,
		logger:       logger,
		adapter:      bluetooth.DefaultAdapter,
	}
}

// Enable powers up the adapter. It is idempotent and safe to call from the
// first request that needs the radio, which is where it belongs: an agent whose
// startup fails because Bluetooth is off is worse than one that says so when
// asked to set up a device.
func (c *Central) Enable() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enableLocked()
}

func (c *Central) enableLocked() error {
	if c.enabled {
		return nil
	}
	if err := c.adapter.Enable(); err != nil {
		return errors.Join(ErrAdapterUnavailable, err)
	}
	c.enabled = true
	return nil
}

// Scan returns every peripheral advertising the provisioning service during
// window, most recently seen first.
//
// IT NEVER ASSUMES ONE DEVICE. The owner may have several StickS3s on the desk,
// and the dashboard is expected to list what came back and let them pick — the
// service UUID is what filters, not the name, exactly as the contract says.
func (c *Central) Scan(ctx context.Context, window time.Duration) ([]Peripheral, error) {
	if window <= 0 {
		window = defaultScanWindow
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enableLocked(); err != nil {
		return nil, err
	}

	var (
		mu    sync.Mutex
		found = map[string]Peripheral{}
	)

	scanCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-scanCtx.Done()
		c.stopScan()
	}()

	err := c.adapter.Scan(func(_ *bluetooth.Adapter, res bluetooth.ScanResult) {
		// Filter on the SERVICE UUID, never on the name: the name is cosmetic
		// and a second product could pick the same one.
		if res.AdvertisementPayload == nil || !res.HasServiceUUID(serviceUUID) {
			return
		}
		p := Peripheral{
			Address: res.Address.String(),
			Name:    res.LocalName(),
			RSSI:    res.RSSI,
			Seen:    time.Now(),
		}
		mu.Lock()
		found[p.Address] = p
		mu.Unlock()
	})
	cancel()
	<-stopped
	if err != nil {
		return nil, fmt.Errorf("bleprov: scan: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]Peripheral, 0, len(found))
	for _, p := range found {
		out = append(out, p)
	}
	sortPeripherals(out)
	c.logger.Info("bleprov: scan finished", "window_sec", int(window.Seconds()), "found", len(out))
	return out, nil
}

// stopScan ends an in-flight scan. StopScan refuses when Scan has not yet
// registered itself, which is a race the library leaves to the caller, so this
// retries briefly rather than losing the stop.
func (c *Central) stopScan() {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := c.adapter.StopScan(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			c.logger.Warn("bleprov: could not stop the scan")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sortPeripherals puts the strongest signal first — on a desk with two devices,
// the one in front of the owner is the one they mean — and breaks ties by
// address so the dashboard list does not shuffle between polls.
func sortPeripherals(v []Peripheral) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && lessPeripheral(v[j], v[j-1]); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func lessPeripheral(a, b Peripheral) bool {
	if a.RSSI != b.RSSI {
		return a.RSSI > b.RSSI
	}
	return a.Address < b.Address
}

// --- provisioning ------------------------------------------------------------

// Phase is where a provisioning run has got to. The dashboard renders these;
// PhasePairing is the one that must put "look at the device screen" in front of
// the owner, because that is the moment a human is being waited on.
type Phase string

const (
	PhaseConnecting  Phase = "connecting"
	PhaseIdentifying Phase = "identifying"
	PhasePairing     Phase = "pairing"
	PhaseSending     Phase = "sending"
	PhaseCommitting  Phase = "committing"
	PhaseApplying    Phase = "applying"
	PhaseDone        Phase = "done"
)

// Progress is one step forward, reported to the caller's callback. It carries
// NO credential value — Sent and Total are byte counts.
type Progress struct {
	Phase  Phase   `json:"phase"`
	Note   string  `json:"note"`
	Sent   int     `json:"sent"`
	Total  int     `json:"total"`
	Info   *Info   `json:"info,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// Result is what a successful run learned.
type Result struct {
	Info      Info          `json:"info"`
	Status    Status        `json:"status"`
	ChunkData int           `json:"chunk_data"`
	Chunks    int           `json:"chunks"`
	Stream    int           `json:"stream_bytes"`
	Elapsed   time.Duration `json:"elapsed"`
}

// Provision runs the whole conversation in docs/BLE_PROVISIONING.md section 8
// against one device, and returns only when the device has reported Applied —
// STORED AND JOINED. Ready is not success: only the hardware knows whether the
// record saved and the radio joined.
//
// rec is wiped from this function's copy before it returns, however it returns.
// The caller still owns its own copy and should Wipe that too.
func (c *Central) Provision(ctx context.Context, address string, rec Record, onProgress func(Progress)) (res *Result, err error) {
	defer rec.Wipe()

	stream, err := Encode(rec)
	if err != nil {
		return nil, err
	}
	begin, err := BeginCommand(len(stream))
	if err != nil {
		return nil, err
	}

	if !c.mu.TryLock() {
		return nil, ErrBusy
	}
	defer c.mu.Unlock()
	if err := c.enableLocked(); err != nil {
		return nil, err
	}

	report := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	started := time.Now()

	var addr bluetooth.Address
	addr.Set(address)
	if addr.String() != address {
		return nil, fmt.Errorf("bleprov: %q is not a device address this Mac knows", address)
	}

	report(Progress{Phase: PhaseConnecting, Note: "Connecting to the device.", Total: len(stream)})
	dev, err := c.adapter.Connect(addr, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("bleprov: connect: %w", err)
	}
	defer func() {
		if derr := dev.Disconnect(); derr != nil {
			c.logger.Warn("bleprov: disconnect", "err", derr)
		}
	}()

	chars, err := discover(dev)
	if err != nil {
		return nil, err
	}

	// --- 3. read Info (open, no pairing) -------------------------------------
	report(Progress{Phase: PhaseIdentifying, Note: "Identifying the device.", Total: len(stream)})
	info, err := readInfo(chars.info)
	if err != nil {
		return nil, err
	}
	if info.Version != Version {
		return nil, fmt.Errorf("%w (device speaks version %d, this agent speaks %d)",
			ErrDeviceVersion, info.Version, Version)
	}
	if int(info.Capacity) < len(stream) {
		// The device publishes its capacity precisely so the daemon never has
		// to guess it — believe the device over the constant.
		return nil, fmt.Errorf("%w: %d bytes for a %d-byte buffer",
			ErrRecordTooLarge, len(stream), info.Capacity)
	}
	c.logger.Info("bleprov: device identified",
		"mac", info.MACString(), "capacity", info.Capacity, "provisioned", info.Provisioned)
	report(Progress{Phase: PhaseIdentifying, Note: "Identifying the device.", Total: len(stream), Info: &info})

	// --- 4. chunk size -------------------------------------------------------
	//
	// GetMTU here is CoreBluetooth's maximum WRITE VALUE length, i.e. ATT_MTU -
	// 3. Any failure falls back to the 19 bytes that always work.
	maxWrite := 0
	if n, mtuErr := chars.data.GetMTU(); mtuErr == nil {
		maxWrite = int(n)
	}
	chunkData := ChunkSize(maxWrite)

	// --- 5. subscribe to Status — THIS IS WHAT STARTS PAIRING ----------------
	watch := newStatusWatch()
	report(Progress{
		Phase: PhasePairing, Total: len(stream), Info: &info,
		Note: "Look at the device screen and type the six digits macOS is asking for.",
	})
	if err := c.subscribe(ctx, chars.status, watch.update); err != nil {
		return nil, err
	}
	defer func() {
		// Best effort: leave the device clean if anything above failed, so a
		// partial credential is not sitting in its RAM waiting for a reboot.
		if err != nil {
			if _, aerr := chars.control.Write(AbortCommand()); aerr != nil {
				c.logger.Warn("bleprov: abort after failure", "err", aerr)
			}
		}
	}()

	// --- 8. BEGIN ------------------------------------------------------------
	if err := writeCharacteristic(chars.control, begin); err != nil {
		return nil, err
	}
	if derr := watch.deviceError(); derr != nil {
		return nil, derr
	}

	// --- 9. the chunks -------------------------------------------------------
	chunks, err := Chunks(stream, chunkData)
	if err != nil {
		return nil, err
	}
	sent := 0
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := writeCharacteristic(chars.data, chunk); err != nil {
			return nil, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err)
		}
		sent += len(chunk) - 1
		// THE FIRST ERROR IS THE ONE YOU GET. The device keeps accepting ATT
		// writes after it has refused the transfer, so a failure shows up in
		// the status and not in the write, and stopping on the first one is
		// what preserves the real reason.
		if derr := watch.deviceError(); derr != nil {
			return nil, derr
		}
		report(Progress{Phase: PhaseSending, Note: "Sending the settings.", Sent: sent, Total: len(stream), Info: &info})
	}

	// --- 10. COMMIT ----------------------------------------------------------
	report(Progress{Phase: PhaseCommitting, Note: "Checking the settings arrived intact.", Sent: sent, Total: len(stream), Info: &info})
	if err := writeCharacteristic(chars.control, CommitCommand()); err != nil {
		return nil, err
	}

	// --- 11. wait for Applied ------------------------------------------------
	report(Progress{Phase: PhaseApplying, Note: "The device is joining the Wi-Fi network.", Sent: sent, Total: len(stream), Info: &info})
	final, err := c.awaitTerminal(ctx, watch, chars.status)
	if err != nil {
		return nil, err
	}
	if final.State != StateApplied {
		return nil, &DeviceError{Code: final.Error, Status: final}
	}

	report(Progress{Phase: PhaseDone, Note: "The device joined the network.", Sent: sent, Total: len(stream), Info: &info, Status: &final})
	c.logger.Info("bleprov: device provisioned",
		"mac", info.MACString(), "stream_bytes", len(stream), "chunks", len(chunks),
		"chunk_data", chunkData, "elapsed_ms", time.Since(started).Milliseconds())

	return &Result{
		Info:      info,
		Status:    final,
		ChunkData: chunkData,
		Chunks:    len(chunks),
		Stream:    len(stream),
		Elapsed:   time.Since(started),
	}, nil
}

// --- GATT plumbing -----------------------------------------------------------

type characteristics struct {
	info    bluetooth.DeviceCharacteristic
	control bluetooth.DeviceCharacteristic
	data    bluetooth.DeviceCharacteristic
	status  bluetooth.DeviceCharacteristic
}

// discover finds the service and its four characteristics. Asking for them by
// UUID in one call means the library returns them in the order requested, so a
// device that is missing one fails here with a clear message rather than later
// with a write to the wrong handle.
func discover(dev bluetooth.Device) (characteristics, error) {
	svcs, err := dev.DiscoverServices([]bluetooth.UUID{serviceUUID})
	if err != nil {
		return characteristics{}, fmt.Errorf("bleprov: this device does not offer the setup service: %w", err)
	}
	if len(svcs) != 1 {
		return characteristics{}, errors.New("bleprov: this device does not offer the setup service")
	}
	cs, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{infoUUID, controlUUID, dataUUID, statusUUID})
	if err != nil {
		return characteristics{}, fmt.Errorf("bleprov: the device's setup service is incomplete: %w", err)
	}
	if len(cs) != 4 {
		return characteristics{}, errors.New("bleprov: the device's setup service is incomplete")
	}
	return characteristics{info: cs[0], control: cs[1], data: cs[2], status: cs[3]}, nil
}

func readInfo(ch bluetooth.DeviceCharacteristic) (Info, error) {
	buf := make([]byte, InfoLen)
	n, err := ch.Read(buf)
	if err != nil {
		return Info{}, fmt.Errorf("bleprov: read device info: %w", err)
	}
	return ParseInfo(buf[:min(n, len(buf))])
}

// writeCharacteristic does a Write REQUEST — acknowledged at the ATT layer — so
// the device is never overrun and the daemon always knows a chunk landed. Write
// Command would be faster and would lose that.
func writeCharacteristic(ch bluetooth.DeviceCharacteristic, value []byte) error {
	if _, err := ch.Write(value); err != nil {
		if isAuthError(err) {
			return ErrBondLost
		}
		return fmt.Errorf("bleprov: write: %w", err)
	}
	return nil
}

// isAuthError recognises the ATT "insufficient authentication/encryption"
// refusal, which the contract says to read as THE BOND WAS LOST rather than as
// a transport failure. CoreBluetooth surfaces it as an NSError string, so the
// match is on text; the phrasings below are Apple's and the Core spec's.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"insufficient authentication",
		"authentication is insufficient",
		"insufficient encryption",
		"encryption is insufficient",
		"not permitted",
		"authorization",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// subscribe enables Status notifications, which is what makes macOS pair.
//
// SUBSCRIBING IS A LONG-RUNNING OPERATION GATED ON A HUMAN, not a fast setup
// call: it writes the CCCD of a characteristic that requires an encrypted,
// MITM-protected link, so the stack must complete pairing before it can honour
// the write. The library gives up after 10 s internally, so this retries until
// PairTimeout — on a device that is already bonded the first attempt returns
// immediately and the owner sees nothing.
func (c *Central) subscribe(ctx context.Context, ch bluetooth.DeviceCharacteristic, onValue func([]byte)) error {
	timeout := c.PairTimeout
	if timeout <= 0 {
		timeout = defaultPairTimeout
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		err := ch.EnableNotifications(onValue)
		if err == nil {
			return nil
		}
		last = err
		if isAuthError(err) {
			return ErrBondLost
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if time.Now().After(deadline) {
			c.logger.Warn("bleprov: pairing never completed", "err", last)
			return ErrPairingTimeout
		}
		time.Sleep(enableRetryInterval)
	}
}

// awaitTerminal waits for Applied or Failed. Notifications are the fast path;
// a poll of the Status characteristic is the safety net, because BLE and Wi-Fi
// share one radio on this board and a notification sent during a join is
// exactly the packet most likely to be lost.
func (c *Central) awaitTerminal(ctx context.Context, w *statusWatch, ch bluetooth.DeviceCharacteristic) (Status, error) {
	timeout := c.ApplyTimeout
	if timeout <= 0 {
		timeout = defaultApplyTimeout
	}
	deadline := time.After(timeout)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()

	for {
		if st, ok := w.terminal(); ok {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-deadline:
			return Status{}, fmt.Errorf("bleprov: the device never reported whether it joined the network (waited %s)", timeout)
		case <-w.changed:
		case <-poll.C:
			buf := make([]byte, StatusLen)
			n, err := ch.Read(buf)
			if err != nil {
				continue
			}
			if st, perr := ParseStatus(buf[:min(n, len(buf))]); perr == nil {
				w.update(buf[:min(n, len(buf))])
				_ = st
			}
		}
	}
}

// --- the status watch --------------------------------------------------------

// statusWatch holds the latest status the device reported. Notifications arrive
// on a CoreBluetooth callback goroutine, so every read of it is behind a mutex.
type statusWatch struct {
	mu      sync.Mutex
	last    Status
	have    bool
	changed chan struct{}
}

func newStatusWatch() *statusWatch {
	return &statusWatch{changed: make(chan struct{}, 1)}
}

func (w *statusWatch) update(value []byte) {
	st, err := ParseStatus(value)
	if err != nil {
		return
	}
	w.mu.Lock()
	w.last = st
	w.have = true
	w.mu.Unlock()
	select {
	case w.changed <- struct{}{}:
	default: // a pending wake-up is as good as a second one
	}
}

// deviceError returns a *DeviceError when the last status says the device
// refused the transfer, and nil otherwise.
func (w *statusWatch) deviceError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.have && w.last.State == StateFailed {
		return &DeviceError{Code: w.last.Error, Status: w.last}
	}
	return nil
}

func (w *statusWatch) terminal() (Status, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.have && w.last.State.Terminal() {
		return w.last, true
	}
	return Status{}, false
}
