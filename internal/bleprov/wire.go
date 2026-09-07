package bleprov

// wire.go — the BLE provisioning wire format, encoder side.
//
// THE GOAL, IN THE OWNER'S WORDS: "once we pair the sticks3 and mac via
// bluetooth, WHY DON'T THE MAC DAEMON SEND EVERYTHING? SSID AND PASSWORD,
// EVERYTHING... DAEMON IP, PORT... IT KNOWS." The user types nothing.
//
// docs/BLE_PROVISIONING.md is the authoritative contract and this file is its
// Go half. The device's decoder is firmware/src/usage/bleprov.{h,cpp}; every
// constant, tag number, error number and length rule below was read out of that
// decoder, not remembered, so the two agree byte for byte. TestWorkedExample in
// wire_test.go rebuilds the hex dump printed in section 8 of the document and
// compares it byte for byte — if this file and that document ever drift, that
// test is what says so.
//
// NOTHING HERE LOGS, FORMATS OR RETURNS A CREDENTIAL VALUE. Record implements
// slog.LogValuer so that even `logger.Info("...", "record", rec)` emits lengths
// only, and errText() returns the same fixed sentences bleprov::errorText()
// does — sentences that cannot contain a submitted byte.

import (
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
)

// --- GATT identity -----------------------------------------------------------
//
// A random 128-bit UUID, generated once and frozen. The four characteristics
// share its suffix and vary only the first 32-bit group, so a packet capture
// reads at a glance. Kept as strings here — central.go parses them — so this
// file stays free of the radio library and is testable with no adapter.
const (
	ServiceUUIDString = "3e12c7ff-ef1e-4bcc-8b8c-0185d4474539"
	InfoUUIDString    = "3e12c701-ef1e-4bcc-8b8c-0185d4474539" // READ, open (no pairing)
	ControlUUIDString = "3e12c702-ef1e-4bcc-8b8c-0185d4474539" // WRITE, encrypted + MITM
	DataUUIDString    = "3e12c703-ef1e-4bcc-8b8c-0185d4474539" // WRITE, encrypted + MITM
	StatusUUIDString  = "3e12c704-ef1e-4bcc-8b8c-0185d4474539" // READ + NOTIFY, encrypted + MITM
)

// --- sizes -------------------------------------------------------------------

const (
	// Version leads the stream AND is repeated in BEGIN, so an unsupported
	// version is refused before a single byte is buffered.
	Version = 1

	// StreamHeader is [version:1][payloadLen:2 LE]; CRCLen is the trailing
	// crc32. MinStream is what BEGIN must declare at the very least.
	StreamHeader = 3
	CRCLen       = 4
	MinStream    = StreamHeader + CRCLen // 7

	// MaxStream is the device's buffer (usage::bleprov::kMaxStream). A BEGIN
	// declaring more is refused with ErrTooLarge. The Info characteristic
	// publishes the real capacity so the daemon never has to assume this one.
	MaxStream = 512

	// SafeChunkData is what a central that never negotiated an MTU can send:
	// ATT_MTU 23, minus the 3-byte ATT_WRITE_REQ header, minus the sequence
	// byte. THE PROTOCOL IS CORRECT AT THIS SIZE AND NEVER REQUIRES MORE.
	SafeChunkData = 19

	// MaxChunkData keeps one write inside ESP_GATT_MAX_ATTR_LEN with room for
	// the controller's own limits: the document's min(ATT_MTU, 244) - 3 - 1.
	MaxChunkData = 244 - 3 - 1
)

// Field capacities, from usage::provision::kMax*. They are not arbitrary: the
// radio caps an SSID at 32 bytes, and the Arduino core copies a WPA2
// passphrase into a 64-byte field WITHOUT terminating it when the source is 64
// bytes or longer, so 63 is the longest safely terminated passphrase.
const (
	MaxSSID    = 32
	MaxPass    = 63
	MaxHost    = 63
	MaxToken   = 64
	MaxOtaPass = 63

	// MinWPAPass is the shortest passphrase the radio will accept. Empty is
	// legal and different — it means an OPEN network.
	MinWPAPass = 8

	// DefaultPort is what a stream carrying no PORT record asks for. It is
	// here to DECODE that absence, never to stand in for the agent's real
	// port: the port always comes from the running configuration.
	DefaultPort = 8765
)

// --- control opcodes ---------------------------------------------------------

const (
	opBegin  = 0x01 // [0x01][version:1][totalLen:2 LE]
	opCommit = 0x02 // [0x02]
	opAbort  = 0x03 // [0x03]

	beginLen = 4
)

// --- TLV tags ----------------------------------------------------------------
//
// Tags below 0x80 are MANDATORY TO UNDERSTAND: a device that meets one it has
// never heard of answers ErrUnknownField rather than shrugging, because
// ignoring a field would report success and behave wrongly. Tags at or above
// 0x80 are skipped, which is the forward-compatibility escape hatch.
const (
	tagSSID    = 0x01 // 1..32, required
	tagPass    = 0x02 // 0..63, empty = open network
	tagHost    = 0x03 // 0..63, empty = discover over mDNS
	tagPort    = 0x04 // exactly 2, little-endian, non-zero
	tagToken   = 0x05 // 0..64, empty = get one by pairing
	tagOtaPass = 0x06 // 0..63, empty = OTA stays disarmed
)

// --- the record --------------------------------------------------------------

// Record is everything the Mac knows and the device needs. Only SSID is
// required, and that asymmetry is deliberate: provision.h draws a line between
// joinable() (an SSID — worth attempting) and complete() (SSID + host + token
// — reachable and authenticated), and a device that joined and has not yet
// been given a token is a legal, normal state.
//
// The BLE daemon normally fills in everything, because it knows everything.
type Record struct {
	SSID     string // required
	Password string // "" is an OPEN network and is legal; 1..7 is refused
	Host     string // "" means "discover the agent over mDNS"
	Port     uint16 // 0 means "the device's default", which is DefaultPort
	Token    string // "" means "get one by pairing"
	OTAPass  string // "" leaves OTA disarmed
}

// LogValue makes Record safe to hand to slog by accident as well as on
// purpose: it renders lengths and never a value. GOLDEN_RULES and AGENTS.md
// house rule 5 both forbid the alternative, and a struct that redacts itself
// cannot be got wrong at a call site.
func (r Record) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("ssid_len", len(r.SSID)),
		slog.Int("pass_len", len(r.Password)),
		slog.String("host", r.Host),
		slog.Int("port", int(r.Port)),
		slog.Int("token_len", len(r.Token)),
		slog.Int("ota_pass_len", len(r.OTAPass)),
	)
}

// Wipe overwrites the credential fields. A provisioning secret has no business
// sitting in memory after it has been sent — the same reason the firmware's
// Decoder::scrub() exists. Go strings are immutable, so this cannot erase the
// backing array the way memset does on the device; it drops the daemon's last
// reference so the value is not readable through this struct and is free to be
// collected.
func (r *Record) Wipe() {
	r.SSID = ""
	r.Password = ""
	r.Host = ""
	r.Token = ""
	r.OTAPass = ""
	r.Port = 0
}

// --- field validation --------------------------------------------------------

// FieldError is a record the DEVICE would refuse, caught here instead. Every
// one carries the exact Err the firmware would have answered with, so the
// dashboard has one switch for local and remote refusals — and catching it
// locally saves the owner a pairing prompt and a round trip to be told the
// SSID was blank.
type FieldError struct {
	Field string // "ssid", "password", "host", "port", "token", "ota_password"
	Code  Err
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Code.String() }

// ErrRecordTooLarge is the local form of ErrTooLarge: the encoded stream does
// not fit the device's buffer. Encode checks against MaxStream; a caller that
// has read Info should check against the capacity the device published.
var ErrRecordTooLarge = errors.New("bleprov: encoded record is larger than the device buffer")

// Validate applies exactly the rules Decoder::parse() applies, in the same
// order, so a record that passes here is one the firmware will accept. It is
// the ONE place those rules are written on this side; Encode calls it.
func (r Record) Validate() error {
	if r.SSID == "" {
		// The firmware distinguishes an absent SSID (MissingSsid) from a
		// present but empty one (FieldEmpty). An empty Go string is never
		// encoded as a TLV at all, so it arrives as the former.
		return &FieldError{Field: "ssid", Code: ErrMissingSSID}
	}
	if len(r.SSID) > MaxSSID {
		return &FieldError{Field: "ssid", Code: ErrFieldTooLong}
	}
	if hasControlChar(r.SSID) {
		return &FieldError{Field: "ssid", Code: ErrControlChar}
	}

	if len(r.Password) > MaxPass {
		return &FieldError{Field: "password", Code: ErrFieldTooLong}
	}
	if hasControlChar(r.Password) {
		return &FieldError{Field: "password", Code: ErrControlChar}
	}
	// Empty is an OPEN network and is legal. 1..7 is a passphrase the radio
	// refuses outright, so storing it would guarantee a join failure the owner
	// cannot diagnose.
	if n := len(r.Password); n > 0 && n < MinWPAPass {
		return &FieldError{Field: "password", Code: ErrPassTooShort}
	}

	if len(r.Host) > MaxHost {
		return &FieldError{Field: "host", Code: ErrFieldTooLong}
	}
	if hasControlChar(r.Host) {
		return &FieldError{Field: "host", Code: ErrControlChar}
	}

	if len(r.Token) > MaxToken {
		return &FieldError{Field: "token", Code: ErrFieldTooLong}
	}
	if hasControlChar(r.Token) {
		return &FieldError{Field: "token", Code: ErrControlChar}
	}

	if len(r.OTAPass) > MaxOtaPass {
		return &FieldError{Field: "ota_password", Code: ErrFieldTooLong}
	}
	if hasControlChar(r.OTAPass) {
		return &FieldError{Field: "ota_password", Code: ErrControlChar}
	}
	return nil
}

// hasControlChar reports whether s contains a byte the device would refuse.
// BYTES 0x80..0xFF ARE ALLOWED: an SSID is UTF-8 and "Café" is a real network
// name. The rule exists because provision::sanitize() CUTS a field at the
// first control byte, so a password containing one would be silently stored as
// a different, shorter password.
func hasControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// --- encoding ----------------------------------------------------------------

// Encode builds the stream the device's decoder expects:
//
//	[version:1][payloadLen:2 LE][payload: TLV records][crc32:4 LE]
//
// EVERY MULTI-BYTE NUMBER IS LITTLE-ENDIAN — the length, the CRC, the port.
// One rule with no exceptions is the rule a second implementation in another
// language gets right.
//
// An empty optional field is OMITTED rather than sent as a zero-length record.
// The two are equivalent to the decoder (a cleared Record has empty strings),
// omission is what the document's worked example does, and it keeps the stream
// short enough that a 306-byte maximum is a real bound.
func Encode(r Record) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}

	payload := make([]byte, 0, 320)
	payload = appendString(payload, tagSSID, r.SSID)
	payload = appendString(payload, tagPass, r.Password)
	payload = appendString(payload, tagHost, r.Host)
	if r.Port != 0 {
		// Absent means "the device's default". We send the agent's REAL port
		// whenever we know it, which is always.
		payload = append(payload, tagPort, 2, byte(r.Port), byte(r.Port>>8))
	}
	payload = appendString(payload, tagToken, r.Token)
	payload = appendString(payload, tagOtaPass, r.OTAPass)

	total := StreamHeader + len(payload) + CRCLen
	if total > MaxStream {
		return nil, ErrRecordTooLarge
	}

	stream := make([]byte, 0, total)
	stream = append(stream, Version, byte(len(payload)), byte(len(payload)>>8))
	stream = append(stream, payload...)

	// CRC-32/ISO-HDLC over [version][payloadLen][payload] — everything before
	// the CRC field. crc32.ChecksumIEEE is exactly the reflected 0xEDB88320,
	// init/xorout 0xFFFFFFFF variant the firmware computes bit by bit.
	sum := crc32.ChecksumIEEE(stream)
	stream = append(stream, byte(sum), byte(sum>>8), byte(sum>>16), byte(sum>>24))
	return stream, nil
}

// appendString adds one TLV record, or nothing when the value is empty. The
// caller has already proved len(v) fits in a byte via Validate.
func appendString(dst []byte, tag byte, v string) []byte {
	if v == "" {
		return dst
	}
	dst = append(dst, tag, byte(len(v)))
	return append(dst, v...)
}

// BeginCommand is the control write that starts (or restarts) a transfer:
// [0x01][version:1][totalLen:2 LE]. BEGIN IS THE UNCONDITIONAL RESTART — it
// clears a previous failure, and it is the only way to retry.
func BeginCommand(total int) ([]byte, error) {
	if total < MinStream {
		return nil, fmt.Errorf("bleprov: stream of %d bytes is below the %d-byte minimum", total, MinStream)
	}
	if total > MaxStream {
		return nil, ErrRecordTooLarge
	}
	return []byte{opBegin, Version, byte(total), byte(total >> 8)}, nil
}

// CommitCommand asks the device to validate what arrived. CommitCommand and
// AbortCommand are functions rather than package-level slices so no caller can
// scribble on a shared array.
func CommitCommand() []byte { return []byte{opCommit} }

// AbortCommand discards the transfer and wipes the device's buffer. It always
// succeeds, from any state: it is what the daemon sends when the owner closes
// the dialog, and it must leave no credential fragment behind.
func AbortCommand() []byte { return []byte{opAbort} }

// ChunkSize turns the largest value one write can carry into the number of
// STREAM bytes per chunk, which is one less because the sequence byte lives in
// the same write.
//
// maxWriteValue is what the transport reports it can put in a single
// un-fragmented ATT write — ATT_MTU minus the 3-byte ATT header. Zero or a
// nonsense value falls back to SafeChunkData, which always works: A CENTRAL
// THAT NEVER NEGOTIATES GETS ATT_MTU 23 AND THIS PROTOCOL IS CORRECT THERE.
//
// The MaxChunkData cap is the document's min(ATT_MTU, 244) - 3 - 1, which
// keeps a write inside ESP_GATT_MAX_ATTR_LEN with room for the controller's
// own limits.
func ChunkSize(maxWriteValue int) int {
	n := maxWriteValue - 1
	if n > MaxChunkData {
		n = MaxChunkData
	}
	if n < SafeChunkData {
		n = SafeChunkData
	}
	return n
}

// Chunks splits a stream into data writes:
//
//	[seq:1][stream bytes:1..N]
//
// seq is 0 for the first chunk after BEGIN and increments by one per chunk,
// WRAPPING MODULO 256 — a stream sent one byte at a time crosses that boundary
// and the firmware's test suite exercises it, so this one does too.
//
// At least one data byte per chunk: an empty chunk would advance the sequence
// for free and the device refuses it.
func Chunks(stream []byte, chunkData int) ([][]byte, error) {
	if len(stream) == 0 {
		return nil, errors.New("bleprov: refusing to chunk an empty stream")
	}
	if chunkData < 1 {
		return nil, fmt.Errorf("bleprov: chunk size %d is below one byte", chunkData)
	}
	out := make([][]byte, 0, (len(stream)+chunkData-1)/chunkData)
	seq := byte(0)
	for off := 0; off < len(stream); off += chunkData {
		end := off + chunkData
		if end > len(stream) {
			end = len(stream)
		}
		w := make([]byte, 0, 1+end-off)
		w = append(w, seq)
		w = append(w, stream[off:end]...)
		out = append(out, w)
		seq++ // byte: wraps modulo 256, which is the documented rule
	}
	return out, nil
}

// --- status and info ---------------------------------------------------------

// State is the device's provisioning state, read from the Status
// characteristic. The numbers are stable across firmware versions.
type State uint8

const (
	StateIdle      State = 0 // nothing in flight
	StateReceiving State = 1 // BEGIN accepted, chunks arriving
	StateReady     State = 2 // COMMIT validated; the record is parsed
	StateApplying  State = 3 // the device is saving and joining
	StateApplied   State = 4 // stored AND joined — provisioning succeeded
	StateFailed    State = 5 // see the error; a fresh BEGIN is required
)

// String returns the same fixed sentence bleprov::stateText() draws on the
// device screen, so the dashboard and the device never disagree about what is
// happening.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "waiting"
	case StateReceiving:
		return "receiving"
	case StateReady:
		return "received"
	case StateApplying:
		return "joining"
	case StateApplied:
		return "done"
	case StateFailed:
		return "failed"
	}
	return "unknown"
}

// Terminal reports whether the device has stopped working on this transfer.
func (s State) Terminal() bool { return s == StateApplied || s == StateFailed }

// StatusLen is the fixed width of the Status characteristic:
// [version:1][state:1][error:1][nextSeq:1][received:2 LE].
const StatusLen = 6

// Status is one read (or notification) of the Status characteristic. NextSeq
// and Received are there so a daemon that lost track can see exactly where the
// device thinks it stands before deciding to start over.
type Status struct {
	Version  uint8
	State    State
	Error    Err
	NextSeq  uint8
	Received uint16
}

// ParseStatus decodes the 6-byte status value. A short value is a protocol
// violation, not something to pad out and guess at.
func ParseStatus(b []byte) (Status, error) {
	if len(b) < StatusLen {
		return Status{}, fmt.Errorf("bleprov: status is %d bytes, want %d", len(b), StatusLen)
	}
	return Status{
		Version:  b[0],
		State:    State(b[1]),
		Error:    Err(b[2]),
		NextSeq:  b[3],
		Received: uint16(b[4]) | uint16(b[5])<<8,
	}, nil
}

// InfoLen is the fixed width of the Info characteristic:
// [version:1][flags:1][capacity:2 LE][mac:6][reserved:2].
const InfoLen = 12

// flagProvisioned is Info's bit 0: the device already holds a record.
const flagProvisioned = 0x01

// Info is the open, unpaired read. NOTHING HERE IS SECRET — the MAC is in
// every advertising packet already, and it is what lets the dashboard say
// WHICH StickS3 it found. It carries the protocol version and the buffer
// capacity so the daemon can decide whether this device is worth putting a
// pairing prompt in front of the owner for.
type Info struct {
	Version     uint8
	Provisioned bool
	Capacity    uint16
	MAC         [6]byte
}

// MACString renders the MAC the way the device's own identity is written,
// lower-case and colon-separated.
func (i Info) MACString() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		i.MAC[0], i.MAC[1], i.MAC[2], i.MAC[3], i.MAC[4], i.MAC[5])
}

// ParseInfo decodes the 12-byte info value. The two reserved bytes are
// deliberately ignored: the contract says they are sent as zero and must be
// ignored on read, which is what makes them usable later.
func ParseInfo(b []byte) (Info, error) {
	if len(b) < InfoLen {
		return Info{}, fmt.Errorf("bleprov: info is %d bytes, want %d", len(b), InfoLen)
	}
	var i Info
	i.Version = b[0]
	i.Provisioned = b[1]&flagProvisioned != 0
	i.Capacity = uint16(b[2]) | uint16(b[3])<<8
	copy(i.MAC[:], b[4:10])
	return i, nil
}

// --- errors ------------------------------------------------------------------

// Err is a device-side refusal code. THE NUMBERS ARE STABLE ACROSS FIRMWARE
// VERSIONS and the Go side switches on the number, never on the text.
type Err uint8

const (
	ErrNone             Err = 0
	ErrMalformedControl Err = 1
	ErrUnknownOp        Err = 2
	ErrBadVersion       Err = 3
	ErrShortStream      Err = 4
	ErrTooLarge         Err = 5
	ErrNotBegun         Err = 6
	ErrMalformedChunk   Err = 7
	ErrOutOfOrder       Err = 8
	ErrOverflow         Err = 9
	ErrTruncated        Err = 10
	ErrLengthMismatch   Err = 11
	ErrBadCRC           Err = 12
	ErrMalformedTLV     Err = 13
	ErrUnknownField     Err = 14
	ErrDuplicateField   Err = 15
	ErrFieldEmpty       Err = 16
	ErrFieldTooLong     Err = 17
	ErrControlChar      Err = 18
	ErrPassTooShort     Err = 19
	ErrBadPort          Err = 20
	ErrMissingSSID      Err = 21
	ErrNotJoinable      Err = 22
	ErrSaveFailed       Err = 23
	ErrJoinFailed       Err = 24
)

// String returns the same fixed sentence bleprov::errorText() returns. Fixed
// by construction: none of them can contain a byte that came off the wire.
func (e Err) String() string {
	switch e {
	case ErrNone:
		return "ok"
	case ErrMalformedControl:
		return "bad command length"
	case ErrUnknownOp:
		return "unknown command"
	case ErrBadVersion:
		return "unsupported format version"
	case ErrShortStream:
		return "declared size too small"
	case ErrTooLarge:
		return "too big for this device"
	case ErrNotBegun:
		return "no transfer in progress"
	case ErrMalformedChunk:
		return "empty or malformed chunk"
	case ErrOutOfOrder:
		return "chunk out of order"
	case ErrOverflow:
		return "more data than declared"
	case ErrTruncated:
		return "transfer incomplete"
	case ErrLengthMismatch:
		return "length does not match"
	case ErrBadCRC:
		return "checksum mismatch"
	case ErrMalformedTLV:
		return "malformed field"
	case ErrUnknownField:
		return "unknown field"
	case ErrDuplicateField:
		return "duplicate field"
	case ErrFieldEmpty:
		return "required field is empty"
	case ErrFieldTooLong:
		return "field too long"
	case ErrControlChar:
		return "illegal character in a field"
	case ErrPassTooShort:
		return "Wi-Fi password too short"
	case ErrBadPort:
		return "invalid port"
	case ErrMissingSSID:
		return "no network name"
	case ErrNotJoinable:
		return "nothing usable to join"
	case ErrSaveFailed:
		return "could not save credentials"
	case ErrJoinFailed:
		return "could not join that network"
	}
	return "unknown error"
}

// Advice is what the daemon (and the dashboard) should DO about this code —
// the last column of the document's error table. GOLDEN_RULES #3 (smart API,
// dumb client) puts it here rather than in the page: a curl of whatever
// endpoint surfaces this gets the complete answer.
//
// ErrJoinFailed is the one the owner will actually hit, so it reads as a
// sentence about their network and never as a code.
func (e Err) Advice() string {
	switch e {
	case ErrNone:
		return ""
	case ErrBadVersion, ErrUnknownField:
		return "This device's firmware is older than this agent — update the firmware."
	case ErrTooLarge:
		return "The settings are too large for this device to store."
	case ErrNotBegun, ErrOutOfOrder, ErrTruncated, ErrBadCRC:
		return "The transfer was interrupted — try setting the device up again."
	case ErrFieldEmpty, ErrMissingSSID, ErrNotJoinable:
		return "This Mac could not work out which Wi-Fi network it is on — join a network and try again."
	case ErrFieldTooLong:
		return "One of the values is too long for this device to store."
	case ErrControlChar:
		return "A value contains a character the device cannot store."
	case ErrPassTooShort:
		return "The Wi-Fi password is shorter than the 8 characters the radio requires."
	case ErrSaveFailed:
		return "The device could not save the credentials — try again, and report it if it repeats."
	case ErrJoinFailed:
		return "The device could not join that Wi-Fi network. The password may be wrong, " +
			"or the device may be out of range of the router."
	}
	// MalformedControl, UnknownOp, ShortStream, MalformedChunk, Overflow,
	// LengthMismatch, MalformedTlv, DuplicateField, BadPort: the document says
	// "bug in the central" for every one of them, and it is right — none can
	// be produced by a well-formed Record.
	return "This is a bug in the agent, not something you did — please report it."
}

// DeviceError is a refusal reported by the device itself, as distinct from a
// FieldError this side caught before transmitting. It carries the whole status
// so a caller can see how far the transfer got.
type DeviceError struct {
	Code   Err
	Status Status
}

func (e *DeviceError) Error() string {
	return "bleprov: device refused the transfer: " + e.Code.String()
}

// Is lets callers write errors.Is(err, &DeviceError{Code: ErrJoinFailed})
// without unwrapping by hand. Only the code is compared: the status varies
// with how far the transfer got and is diagnostic, not identity.
func (e *DeviceError) Is(target error) bool {
	other, ok := target.(*DeviceError)
	return ok && other.Code == e.Code
}
