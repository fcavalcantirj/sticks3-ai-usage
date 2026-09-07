package bleprov

import (
	"bytes"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

// The worked example from docs/BLE_PROVISIONING.md section 8, transcribed from
// the document rather than produced by this encoder. It is the single most
// valuable test in this file: the document says the firmware and the daemon
// agree on these exact bytes, and this is what proves the Go half does.
//
// Fabricated values throughout — no real credential appears in this repo.
const (
	exampleSSID  = "HomeNet"
	examplePass  = "correcthorse"
	exampleHost  = "192.168.0.42"
	examplePort  = 8765
	exampleToken = "0123456789abcdef0123456789abcdef"

	// The stream, transcribed row by row from the document's hex dump.
	exampleStreamHex = "014b000107486f6d654e6574020c636f" +
		"7272656374686f727365030c3139322e" +
		"3136382e302e343204023d2205203031" +
		"32333435363738396162636465663031" +
		"323334353637383961626364656676f6" +
		"7276"
)

func exampleRecord() Record {
	return Record{
		SSID:     exampleSSID,
		Password: examplePass,
		Host:     exampleHost,
		Port:     examplePort,
		Token:    exampleToken,
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex in test data: %v", err)
	}
	return b
}

// --- the checksum both languages must agree on -------------------------------

// TestCRCCheckVector pins the published CRC-32/ISO-HDLC check value. A checksum
// two implementations must agree on is the single easiest thing in this
// protocol to get subtly wrong, so it gets its own test on both sides — this is
// the same vector firmware/test/host/test_bleprov.cpp asserts.
func TestCRCCheckVector(t *testing.T) {
	if got := crc32.ChecksumIEEE([]byte("123456789")); got != 0xCBF43926 {
		t.Fatalf("crc32(\"123456789\") = %#08x, want 0xCBF43926", got)
	}
}

// --- the worked example ------------------------------------------------------

func TestWorkedExampleStream(t *testing.T) {
	want := mustHex(t, exampleStreamHex)
	got, err := Encode(exampleRecord())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stream mismatch\n got %s\nwant %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
	if len(got) != 82 {
		t.Fatalf("totalLen = %d, want 82", len(got))
	}
	// payloadLen is little-endian and covers the TLV region only.
	if payloadLen := int(got[1]) | int(got[2])<<8; payloadLen != 75 {
		t.Fatalf("payloadLen = %d, want 75", payloadLen)
	}
	if StreamHeader+75+CRCLen != len(got) {
		t.Fatal("3 + payloadLen + 4 must equal totalLen, or the device answers LengthMismatch")
	}
}

func TestWorkedExampleBegin(t *testing.T) {
	stream, err := Encode(exampleRecord())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	begin, err := BeginCommand(len(stream))
	if err != nil {
		t.Fatalf("BeginCommand: %v", err)
	}
	want := []byte{0x01, 0x01, 0x52, 0x00} // "Control BEGIN: 01 01 52 00"
	if !bytes.Equal(begin, want) {
		t.Fatalf("BEGIN = % x, want % x", begin, want)
	}
}

// TestWorkedExampleChunks reproduces the five MTU-23 data writes printed in the
// document, byte for byte including the leading sequence number.
func TestWorkedExampleChunks(t *testing.T) {
	stream, err := Encode(exampleRecord())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	chunks, err := Chunks(stream, SafeChunkData)
	if err != nil {
		t.Fatalf("Chunks: %v", err)
	}
	wantChunks := []string{
		"00 01 4B 00 01 07 48 6F 6D 65 4E 65 74 02 0C 63 6F 72 72 65",
		"01 63 74 68 6F 72 73 65 03 0C 31 39 32 2E 31 36 38 2E 30 2E",
		"02 34 32 04 02 3D 22 05 20 30 31 32 33 34 35 36 37 38 39 61",
		"03 62 63 64 65 66 30 31 32 33 34 35 36 37 38 39 61 62 63 64",
		"04 65 66 76 F6 72 76",
	}
	if len(chunks) != len(wantChunks) {
		t.Fatalf("got %d chunks, want %d", len(chunks), len(wantChunks))
	}
	for i, w := range wantChunks {
		expect := mustHex(t, strings.ToLower(strings.ReplaceAll(w, " ", "")))
		if !bytes.Equal(chunks[i], expect) {
			t.Fatalf("chunk #%d = % X, want % X", i, chunks[i], expect)
		}
	}
}

func TestWorkedExampleStatus(t *testing.T) {
	// "Status after COMMIT:  01 02 00 05 52 00"
	st, err := ParseStatus(mustHex(t, "010200055200"))
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if st.Version != 1 || st.State != StateReady || st.Error != ErrNone {
		t.Fatalf("status = %+v, want version 1 / Ready / None", st)
	}
	if st.NextSeq != 5 || st.Received != 82 {
		t.Fatalf("nextSeq/received = %d/%d, want 5/82", st.NextSeq, st.Received)
	}
	if st.State.String() != "received" {
		t.Fatalf("State(Ready).String() = %q, want %q", st.State.String(), "received")
	}
}

func TestWorkedExampleInfo(t *testing.T) {
	// "Info (MAC 24:0A:C4:11:D5:34, unprovisioned): 01 00 00 02 24 0A C4 11 D5 34 00 00"
	info, err := ParseInfo(mustHex(t, "01000002240ac411d5340000"))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if info.Version != 1 || info.Provisioned {
		t.Fatalf("info = %+v, want version 1 and unprovisioned", info)
	}
	if info.Capacity != MaxStream {
		t.Fatalf("capacity = %d, want %d", info.Capacity, MaxStream)
	}
	if got := info.MACString(); got != "24:0a:c4:11:d5:34" {
		t.Fatalf("MACString = %q, want 24:0a:c4:11:d5:34", got)
	}
}

func TestParseInfoProvisionedFlag(t *testing.T) {
	info, err := ParseInfo(mustHex(t, "01010002240ac411d5340000"))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if !info.Provisioned {
		t.Fatal("flags bit 0 set must read as provisioned")
	}
}

// --- framing rules -----------------------------------------------------------

func TestEncodeOmitsEmptyOptionalFields(t *testing.T) {
	// Only an SSID: the lowest bar the protocol accepts, and a legal, normal
	// state for a device that has joined and not yet been given a token.
	stream, err := Encode(Record{SSID: "n"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := []byte{Version, 3, 0, tagSSID, 1, 'n'}
	if !bytes.Equal(stream[:len(want)], want) {
		t.Fatalf("stream head = % X, want % X", stream[:len(want)], want)
	}
	if len(stream) != StreamHeader+3+CRCLen {
		t.Fatalf("len = %d, want %d", len(stream), StreamHeader+3+CRCLen)
	}
}

func TestEncodePortIsLittleEndian(t *testing.T) {
	stream, err := Encode(Record{SSID: "n", Port: 0x223D})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// [ver][len:2][01 01 'n'][04 02 3D 22][crc:4]
	if got := stream[6:10]; !bytes.Equal(got, []byte{tagPort, 2, 0x3D, 0x22}) {
		t.Fatalf("port TLV = % X, want 04 02 3D 22", got)
	}
}

func TestEncodeZeroPortIsOmitted(t *testing.T) {
	// Absent means "the device's default", which is what a zero port asks for.
	stream, err := Encode(Record{SSID: "n", Port: 0})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if bytes.IndexByte(stream[StreamHeader:len(stream)-CRCLen], tagPort) >= 0 {
		t.Fatal("a zero port must not be encoded — the device would answer BadPort")
	}
}

func TestEncodeCRCIsOverHeaderAndPayloadOnly(t *testing.T) {
	stream, err := Encode(exampleRecord())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	body := stream[:len(stream)-CRCLen]
	want := crc32.ChecksumIEEE(body)
	got := uint32(stream[len(stream)-4]) | uint32(stream[len(stream)-3])<<8 |
		uint32(stream[len(stream)-2])<<16 | uint32(stream[len(stream)-1])<<24
	if got != want {
		t.Fatalf("crc = %#08x, want %#08x", got, want)
	}
	if want != 0x7672F676 {
		t.Fatalf("worked-example crc = %#08x, want 0x7672F676", want)
	}
}

// TestLargestLegalStream pins the 306-byte figure the document asserts, so the
// number cannot drift away from the field capacities on either side.
func TestLargestLegalStream(t *testing.T) {
	stream, err := Encode(Record{
		SSID:     strings.Repeat("s", MaxSSID),
		Password: strings.Repeat("p", MaxPass),
		Host:     strings.Repeat("h", MaxHost),
		Port:     8765,
		Token:    strings.Repeat("t", MaxToken),
		OTAPass:  strings.Repeat("o", MaxOtaPass),
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(stream) != 306 {
		t.Fatalf("largest legal stream = %d bytes, want 306", len(stream))
	}
	if len(stream) > MaxStream {
		t.Fatal("the largest legal stream must fit the device buffer")
	}
}

// --- chunking ----------------------------------------------------------------

func TestChunksSequenceWrapsModulo256(t *testing.T) {
	// A stream sent one byte at a time crosses the byte boundary. The firmware
	// test suite exercises this; so must this side, or the two disagree at
	// exactly the point nobody looks.
	stream, err := Encode(Record{
		SSID:     strings.Repeat("s", MaxSSID),
		Password: strings.Repeat("p", MaxPass),
		Host:     strings.Repeat("h", MaxHost),
		Port:     8765,
		Token:    strings.Repeat("t", MaxToken),
		OTAPass:  strings.Repeat("o", MaxOtaPass),
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(stream) <= 256 {
		t.Fatalf("need a stream longer than 256 bytes to cross the wrap, got %d", len(stream))
	}
	chunks, err := Chunks(stream, 1)
	if err != nil {
		t.Fatalf("Chunks: %v", err)
	}
	if len(chunks) != len(stream) {
		t.Fatalf("got %d chunks, want %d", len(chunks), len(stream))
	}
	for i, c := range chunks {
		if len(c) != 2 {
			t.Fatalf("chunk #%d is %d bytes, want 2", i, len(c))
		}
		if c[0] != byte(i) {
			t.Fatalf("chunk #%d seq = %d, want %d (wrapped)", i, c[0], byte(i))
		}
		if c[1] != stream[i] {
			t.Fatalf("chunk #%d payload = %#x, want %#x", i, c[1], stream[i])
		}
	}
	if chunks[256][0] != 0 {
		t.Fatalf("seq after 255 = %d, want 0", chunks[256][0])
	}
}

func TestChunksReassembleToTheStream(t *testing.T) {
	stream, err := Encode(exampleRecord())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, size := range []int{1, 2, 7, SafeChunkData, 20, 100, len(stream), len(stream) + 50} {
		chunks, err := Chunks(stream, size)
		if err != nil {
			t.Fatalf("Chunks(%d): %v", size, err)
		}
		var got []byte
		for i, c := range chunks {
			if len(c) < 2 {
				t.Fatalf("size %d chunk #%d has no data byte — the device answers MalformedChunk", size, i)
			}
			if len(c)-1 > size {
				t.Fatalf("size %d chunk #%d carries %d data bytes", size, i, len(c)-1)
			}
			if c[0] != byte(i) {
				t.Fatalf("size %d chunk #%d seq = %d, want %d", size, i, c[0], byte(i))
			}
			got = append(got, c[1:]...)
		}
		if !bytes.Equal(got, stream) {
			t.Fatalf("size %d: reassembled stream differs from the original", size)
		}
	}
}

func TestChunksRefusesNonsense(t *testing.T) {
	if _, err := Chunks(nil, SafeChunkData); err == nil {
		t.Fatal("an empty stream must be refused")
	}
	if _, err := Chunks([]byte{1, 2, 3}, 0); err == nil {
		t.Fatal("a zero chunk size must be refused")
	}
}

func TestChunkSize(t *testing.T) {
	tests := []struct {
		name          string
		maxWriteValue int
		want          int
	}{
		// A central that never negotiates gets ATT_MTU 23: 20 bytes of value,
		// 19 of stream. This row is the whole correctness claim.
		{"unnegotiated ATT_MTU 23", 20, SafeChunkData},
		{"nothing reported", 0, SafeChunkData},
		{"transport error, negative", -1, SafeChunkData},
		{"absurdly small", 3, SafeChunkData},
		{"exactly the floor", 20, 19},
		{"one above the floor", 21, 20},
		{"typical negotiated 185", 182, 181},
		{"capped at the 244 rule", 512, MaxChunkData},
		{"cap boundary", MaxChunkData + 1, MaxChunkData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChunkSize(tc.maxWriteValue); got != tc.want {
				t.Fatalf("ChunkSize(%d) = %d, want %d", tc.maxWriteValue, got, tc.want)
			}
		})
	}
}

// --- control commands --------------------------------------------------------

func TestControlCommandLengths(t *testing.T) {
	// A control write whose length does not match its opcode is refused by the
	// device, never guessed at.
	begin, err := BeginCommand(MinStream)
	if err != nil {
		t.Fatalf("BeginCommand: %v", err)
	}
	if len(begin) != beginLen {
		t.Fatalf("BEGIN is %d bytes, want %d", len(begin), beginLen)
	}
	if begin[1] != Version {
		t.Fatalf("BEGIN version = %d, want %d", begin[1], Version)
	}
	if len(CommitCommand()) != 1 || CommitCommand()[0] != opCommit {
		t.Fatalf("COMMIT = % X, want 02", CommitCommand())
	}
	if len(AbortCommand()) != 1 || AbortCommand()[0] != opAbort {
		t.Fatalf("ABORT = % X, want 03", AbortCommand())
	}
	// Returned fresh each call so a caller cannot scribble on a shared array.
	a := CommitCommand()
	a[0] = 0xFF
	if CommitCommand()[0] != opCommit {
		t.Fatal("CommitCommand must not share backing storage between calls")
	}
}

func TestBeginCommandBounds(t *testing.T) {
	if _, err := BeginCommand(MinStream - 1); err == nil {
		t.Fatal("a total below the 7-byte minimum must be refused (ShortStream)")
	}
	if _, err := BeginCommand(MaxStream + 1); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("a total above the device buffer must be ErrRecordTooLarge, got %v", err)
	}
}

// --- validation --------------------------------------------------------------

func TestValidateMirrorsTheFirmware(t *testing.T) {
	tests := []struct {
		name  string
		rec   Record
		field string
		code  Err
	}{
		{"no ssid", Record{}, "ssid", ErrMissingSSID},
		{"ssid too long", Record{SSID: strings.Repeat("s", MaxSSID+1)}, "ssid", ErrFieldTooLong},
		{"ssid with a tab", Record{SSID: "a\tb"}, "ssid", ErrControlChar},
		{"ssid with a NUL", Record{SSID: "a\x00b"}, "ssid", ErrControlChar},
		{"ssid with DEL", Record{SSID: "a\x7fb"}, "ssid", ErrControlChar},
		{"pass too short", Record{SSID: "n", Password: "1234567"}, "password", ErrPassTooShort},
		{"pass too long", Record{SSID: "n", Password: strings.Repeat("p", MaxPass+1)}, "password", ErrFieldTooLong},
		{"pass with a newline", Record{SSID: "n", Password: "abcdefg\n"}, "password", ErrControlChar},
		{"host too long", Record{SSID: "n", Host: strings.Repeat("h", MaxHost+1)}, "host", ErrFieldTooLong},
		{"token too long", Record{SSID: "n", Token: strings.Repeat("t", MaxToken+1)}, "token", ErrFieldTooLong},
		{"ota pass too long", Record{SSID: "n", OTAPass: strings.Repeat("o", MaxOtaPass+1)}, "ota_password", ErrFieldTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rec.Validate()
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Validate() = %v, want a *FieldError", err)
			}
			if fe.Field != tc.field || fe.Code != tc.code {
				t.Fatalf("Validate() = %s/%v, want %s/%v", fe.Field, fe.Code, tc.field, tc.code)
			}
			if _, err := Encode(tc.rec); err == nil {
				t.Fatal("Encode must refuse what Validate refuses")
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
	}{
		{"ssid only", Record{SSID: "n"}},
		{"open network: empty password is legal", Record{SSID: "n", Password: ""}},
		{"exactly the WPA minimum", Record{SSID: "n", Password: "12345678"}},
		{"every field at capacity", Record{
			SSID:     strings.Repeat("s", MaxSSID),
			Password: strings.Repeat("p", MaxPass),
			Host:     strings.Repeat("h", MaxHost),
			Port:     8765,
			Token:    strings.Repeat("t", MaxToken),
			OTAPass:  strings.Repeat("o", MaxOtaPass),
		}},
		// Bytes 0x80..0xFF are ALLOWED: an SSID is UTF-8 and "Café" is a real
		// network name.
		{"utf-8 ssid", Record{SSID: "Café"}},
		{"utf-8 password", Record{SSID: "n", Password: "sênha-do-café"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rec.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if _, err := Encode(tc.rec); err != nil {
				t.Fatalf("Encode: %v", err)
			}
		})
	}
}

// --- status/info parsing -----------------------------------------------------

func TestParseStatusRefusesShortValues(t *testing.T) {
	for n := 0; n < StatusLen; n++ {
		if _, err := ParseStatus(make([]byte, n)); err == nil {
			t.Fatalf("a %d-byte status must be refused", n)
		}
	}
}

func TestParseInfoRefusesShortValues(t *testing.T) {
	for n := 0; n < InfoLen; n++ {
		if _, err := ParseInfo(make([]byte, n)); err == nil {
			t.Fatalf("a %d-byte info must be refused", n)
		}
	}
}

func TestParseInfoIgnoresReservedBytes(t *testing.T) {
	// "reserved is sent as zero and must be ignored on read" — which is what
	// makes those two bytes usable by a future firmware without a flag day.
	a, err := ParseInfo(mustHex(t, "01000002240ac411d5340000"))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	b, err := ParseInfo(mustHex(t, "01000002240ac411d534ffff"))
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if a != b {
		t.Fatalf("reserved bytes changed the parse: %+v vs %+v", a, b)
	}
}

func TestStateTerminal(t *testing.T) {
	for _, s := range []State{StateIdle, StateReceiving, StateReady, StateApplying} {
		if s.Terminal() {
			t.Fatalf("State(%d) must not be terminal — the dashboard would stop waiting", s)
		}
	}
	for _, s := range []State{StateApplied, StateFailed} {
		if !s.Terminal() {
			t.Fatalf("State(%d) must be terminal", s)
		}
	}
}

// --- error texts -------------------------------------------------------------

// TestErrTextsMatchTheFirmware pins every sentence against
// firmware/src/usage/bleprov.cpp errorText(). The device draws these on its
// screen and the dashboard prints them; if the two ever differ, a user
// comparing the two is being told the same failure has two names.
func TestErrTextsMatchTheFirmware(t *testing.T) {
	want := map[Err]string{
		ErrNone: "ok", ErrMalformedControl: "bad command length", ErrUnknownOp: "unknown command",
		ErrBadVersion: "unsupported format version", ErrShortStream: "declared size too small",
		ErrTooLarge: "too big for this device", ErrNotBegun: "no transfer in progress",
		ErrMalformedChunk: "empty or malformed chunk", ErrOutOfOrder: "chunk out of order",
		ErrOverflow: "more data than declared", ErrTruncated: "transfer incomplete",
		ErrLengthMismatch: "length does not match", ErrBadCRC: "checksum mismatch",
		ErrMalformedTLV: "malformed field", ErrUnknownField: "unknown field",
		ErrDuplicateField: "duplicate field", ErrFieldEmpty: "required field is empty",
		ErrFieldTooLong: "field too long", ErrControlChar: "illegal character in a field",
		ErrPassTooShort: "Wi-Fi password too short", ErrBadPort: "invalid port",
		ErrMissingSSID: "no network name", ErrNotJoinable: "nothing usable to join",
		ErrSaveFailed: "could not save credentials", ErrJoinFailed: "could not join that network",
	}
	for code, text := range want {
		if got := code.String(); got != text {
			t.Fatalf("Err(%d).String() = %q, want %q", code, got, text)
		}
	}
	if got := Err(200).String(); got != "unknown error" {
		t.Fatalf("an unknown code must not render as a number, got %q", got)
	}
	// Every defined code must carry advice; ErrNone deliberately carries none.
	for code := ErrMalformedControl; code <= ErrJoinFailed; code++ {
		if code.Advice() == "" {
			t.Fatalf("Err(%d) has no advice for the dashboard to show", code)
		}
	}
	if ErrNone.Advice() != "" {
		t.Fatal("ErrNone must have no advice")
	}
}

func TestDeviceErrorIs(t *testing.T) {
	err := error(&DeviceError{Code: ErrJoinFailed, Status: Status{State: StateFailed}})
	if !errors.Is(err, &DeviceError{Code: ErrJoinFailed}) {
		t.Fatal("errors.Is must match on the code")
	}
	if errors.Is(err, &DeviceError{Code: ErrBadCRC}) {
		t.Fatal("errors.Is must not match a different code")
	}
	if !strings.Contains(err.Error(), "could not join that network") {
		t.Fatalf("DeviceError.Error() = %q, want the fixed sentence", err.Error())
	}
}

// --- the rule that matters most ----------------------------------------------

// TestRecordLogValueNeverCarriesASecret is the guard on AGENTS.md house rule 5
// and the task's own hardest constraint. Record is passed to slog by the
// central; if it ever rendered a value, a Wi-Fi password would land in the
// launchd log.
func TestRecordLogValueNeverCarriesASecret(t *testing.T) {
	rec := Record{
		SSID:     "sekrit-ssid",
		Password: "sekrit-password",
		Host:     "192.168.0.42",
		Port:     8765,
		Token:    "sekrit-token",
		OTAPass:  "sekrit-ota",
	}
	rendered := rec.LogValue().String()
	for _, secret := range []string{"sekrit-password", "sekrit-token", "sekrit-ota", "sekrit-ssid"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q: %s", secret, rendered)
		}
	}
	for _, want := range []string{"pass_len=15", "token_len=12", "ssid_len=11"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("LogValue = %s, want it to contain %q", rendered, want)
		}
	}
}

func TestRecordWipe(t *testing.T) {
	rec := exampleRecord()
	rec.Wipe()
	if rec != (Record{}) {
		t.Fatalf("Wipe left %+v", rec)
	}
}
