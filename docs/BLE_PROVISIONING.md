# BLE provisioning — the wire contract

The Mac daemon knows everything the device needs: the Wi-Fi SSID, the Wi-Fi password, its
own address and port, and a token it can mint on the spot. This document is how it hands
all of that to a StickS3 that has just been flashed from M5Burner and knows nothing.

**The user types nothing.** They flash the device, open the dashboard, and click once.

```
  device                                        daemon (Mac)
  ──────                                        ────────────
  boots unprovisioned
  advertises "usaged-D534"        ────────────► dashboard shows "StickS3 found — set up"
  shows a 6-digit passkey         ◄──────────── owner clicks; macOS asks for the passkey
  link is bonded + encrypted      ◄───────────► owner types the 6 digits off the screen
                                  ◄──────────── BEGIN, chunks, COMMIT
  joins Wi-Fi, saves to NVS       ────────────► status notify: Applied
  stops advertising, fetches
```

Two halves implement this. **The pure half** — `firmware/src/usage/bleprov.{h,cpp}`, host
tested by `make fw-test` — owns every byte and every verdict in this document. **The HAL
half** — `firmware/src/hal/sticks3/bleprov.*` — owns the GATT server, the pairing dialogue
and the screen, and contains no parsing at all. Anything below that is a *rule* is
enforced in the pure half and has a test in `firmware/test/host/test_bleprov*.cpp`.

- [TEST] Everything in "The payload", "Framing" and "Errors" is exercised by 71 host
  tests, including the CRC-32 check vector both languages must agree on.
- [REAL] The Arduino-core facts cited below were read on disk from
  `~/.platformio/packages/framework-arduinoespressif32` (Arduino 2.0.17 / IDF 4.4.7) on
  2026-09-06, with file and line references.
- [REAL] The BLE stack costs **+578,928 bytes of flash and +20,904 bytes of RAM** on top
  of the current firmware, landing at **50.2%** of the app partition. It fits.
- [UNVERIFIED] Everything under "What must still be measured on hardware". In this project
  that label means *do not build on it yet* — four defects on 2026-09-06 were documented
  behaviour that turned out wrong on the wire.

---

## 1. GATT

### Service

```
usaged provisioning      3e12c7ff-ef1e-4bcc-8b8c-0185d4474539
```

A random 128-bit UUID, not a SIG-assigned one, generated once and frozen. The four
characteristics share its suffix and vary only the first 32-bit group, so a packet capture
reads at a glance:

| characteristic | UUID | properties | ATT permissions |
|---|---|---|---|
| **Info** | `3e12c701-ef1e-4bcc-8b8c-0185d4474539` | READ | open (no pairing) |
| **Control** | `3e12c702-ef1e-4bcc-8b8c-0185d4474539` | WRITE | encrypted + MITM |
| **Data** | `3e12c703-ef1e-4bcc-8b8c-0185d4474539` | WRITE | encrypted + MITM |
| **Status** | `3e12c704-ef1e-4bcc-8b8c-0185d4474539` | READ, NOTIFY | encrypted + MITM |

**Info is deliberately readable without pairing.** It carries the protocol version, the
buffer capacity and the BLE MAC — all of which are already in the advertising packet or
derivable from it — so the daemon can decide *whether this device is worth pairing with*
before it puts a pairing prompt in front of the owner. It carries nothing else.

Status needs a **CCCD** for notifications: the Arduino BLE library does not add one, so the
HAL must `addDescriptor(new BLE2902())` ([REAL] `BLE2902.h` ships in the same directory).

### Advertising

- Local name: the same `usaged-XXXX` identity the captive portal AP uses
  (`usage::portal::apIdentity`), so one device has one name however it is set up.
- The service UUID above is in the advertising payload, so the daemon filters on it rather
  than on the name.
- **Advertising runs only while the device is unprovisioned**, plus a bounded window the
  owner opens deliberately (a button hold) on a device that is already provisioned. A
  provisioned device that advertises forever is a permanent invitation.
- One connection at a time. A second central is refused while a transfer is in flight.

---

## 2. Security

### The decision

**Bonding with a passkey displayed on the device screen — the BLE stack's own pairing, no
invented crypto.**

The device sets `ESP_LE_AUTH_REQ_SC_MITM_BOND` (LE Secure Connections, MITM protection,
bonding — [REAL] `esp_gap_ble_api.h:51`) and I/O capability `ESP_IO_CAP_OUT`, which is
DisplayOnly ([REAL] `esp_gap_ble_api.h:61`). The stack then generates a random six-digit
passkey and calls `BLESecurityCallbacks::onPassKeyNotify(uint32_t)` ([REAL]
`BLESecurity.h`), and the HAL paints it on the LCD via `bleprov::formatPasskey()`, which
zero-pads to six digits because that is what the central's dialog expects.

macOS then prompts for those six digits. **For the owner that is one click and six digits
off a screen they are already holding** — versus typing an SSID, a password, an IP address
and a 32-character token into a phone.

### Why an unbonded central must not be able to write

`BLECharacteristic`'s default permissions are `ESP_GATT_PERM_READ | ESP_GATT_PERM_WRITE`
([REAL] `BLECharacteristic.h`, the `m_permissions` initialiser) — **open to any central
that can connect**. Any BLE peripheral in range accepts a connection from anyone; that is
the protocol, not a bug. So the HAL must call, on Control, Data and Status:

```cpp
setAccessPermissions(ESP_GATT_PERM_READ_ENC_MITM | ESP_GATT_PERM_WRITE_ENC_MITM);
```

([REAL] `esp_gatt_defs.h:279-283` defines those bits.) Without that call the entire pairing
step is theatre: an unpaired stranger could write a record and point the device at their
own agent. **This is the single line that makes the design a design.**

The threat this closes is a stranger in radio range provisioning the device to their own
network or their own agent. It does **not** claim to defend against someone holding the
device, who can read the screen anyway.

### What the daemon must do

1. Pair and bond on first contact; store the bond.
2. Reuse the bond on later connections — the owner types the passkey **once per device**,
   not once per session.
3. Treat "insufficient authentication" on a write as *the bond was lost* (the device was
   factory-reset, or the Mac's bond store was cleared), drop the bond and re-pair.

### The fallback, if MITM pairing proves unworkable on macOS

Documented now so it is a decision and not an improvisation: fall back to
`ESP_GATT_PERM_WRITE_ENCRYPTED` with Just Works pairing **plus a physical confirmation** —
the device shows "Set up from this Mac? hold BtnA" and refuses every write until the button
is held. That keeps a human in the loop with no typing at all. It is strictly weaker
(Just Works has no MITM protection) and is the second choice, not the first.

### What is never allowed, in either half

- No credential VALUE is ever logged, drawn, or returned by a getter. The serial line
  carries lengths and `set`/`unset`, exactly as the `[CREDS]` line already does.
- `bleprov::errorText()` and `stateText()` return fixed sentences that cannot contain a
  submitted byte, for the same reason `portal::rejectText()` does.
- `Decoder::scrub()` zeroes the stream buffer and the record the moment the record is
  persisted, keeping only the state and error the daemon is still waiting to read. A
  provisioning secret has no business sitting in RAM for the rest of the uptime.

---

## 3. MTU

**The protocol is correct at ATT_MTU 23 and never requires more.**

A central that never negotiates gets ATT_MTU 23. An ATT Write Request spends 3 bytes on
opcode and handle, leaving **20 bytes of value**, of which the sequence byte takes one:
**19 bytes of stream per chunk** (`bleprov::kSafeChunkData`). A full record needs 5 such
chunks; the worked example below is exactly that.

The daemon **may** negotiate a larger MTU and send bigger chunks — the device checks only
that the running total stays within what BEGIN declared, and a single 306-byte chunk is
legal. It must not *assume* it can. Recommended chunk size:

```
chunkData = min(negotiatedATTMTU, 244) - 3 - 1
```

The 244 cap keeps a write inside `ESP_GATT_MAX_ATTR_LEN` (600, [REAL]
`esp_gatt_defs.h:304`) with room for the controller's own limits.

**Do not use ATT Long Write (prepare/execute) to send the record in one attribute.** [REAL]
`BLECharacteristic.cpp:289-290` accumulates prepared fragments with
`m_value.addPart(param->write.value, param->write.len)` and **ignores
`param->write.offset`** — the fragments are concatenated in arrival order with no offset
check at all. The chunking in this protocol is at the application layer precisely so that
ordering is verified by something that actually verifies it.

**Use Write Request, not Write Command.** Every chunk is acknowledged at the ATT layer, so
the device is never overrun and the daemon always knows a chunk landed.

---

## 4. Framing

### Control writes

One opcode byte, then fixed operands. **A control write whose length does not match its
opcode is refused, never guessed at.**

| opcode | name | length | operands |
|---|---|---|---|
| `0x01` | BEGIN | 4 | `[0x01][version:1][totalLen:2 LE]` |
| `0x02` | COMMIT | 1 | — |
| `0x03` | ABORT | 1 | — |

- **BEGIN** is the unconditional restart. It resets the decoder — including out of a
  previous failure — checks the version before a single byte is buffered, and checks
  `totalLen` against 7 (the minimum stream) and 512 (`kMaxStream`, the device's buffer). It
  is the only way to start, and the only way to retry.
- **COMMIT** validates what arrived: the received count must equal `totalLen`, the header's
  payload length must agree with it, the CRC must match, and the payload must parse into a
  record that satisfies `provision::joinable()`.
- **ABORT** always succeeds, from any state, and wipes the buffer. It is what the daemon
  sends when the owner closes the dialog.

### Data writes

```
[seq:1][stream bytes:1..N]
```

- `seq` is `0` for the first chunk after BEGIN and increments by one per chunk, **wrapping
  modulo 256** — a stream sent one byte at a time crosses that boundary and the test suite
  exercises it.
- At least one data byte. An empty chunk would advance the sequence for free and is
  refused.
- Any chunk arriving outside the Receiving state is `NotBegun`. A wrong sequence number is
  `OutOfOrder`. A chunk that would push the total past `totalLen` is `Overflow`.

**Any error kills the whole transfer.** There is no per-chunk retry: the daemon starts
over with BEGIN. Re-sending ~300 bytes over BLE is cheap, and a resync protocol is the kind
of cleverness that fails once in a hundred setups and is never debugged. A *repeat* of the
chunk just accepted is `OutOfOrder` too, for the same reason.

**The first error is the one you get.** Chunks already in the central's queue keep arriving
after a failure; each of them would otherwise overwrite the real reason with `NotBegun`
before the daemon ever reads the status. The HAL's own outcomes (`SaveFailed`,
`JoinFailed`) are the exception — those are authoritative and do overwrite.

---

## 5. The stream

```
[version:1][payloadLen:2 LE][payload: payloadLen bytes][crc32:4 LE]
```

- `version` is `1` and must equal the version BEGIN declared. It leads the stream so a
  future daemon can change everything after it.
- `payloadLen` is the TLV region only — not the header, not the CRC.
- `totalLen` (what BEGIN declares) is `3 + payloadLen + 4`. Both are sent and they must
  agree, or `LengthMismatch`.
- **Every multi-byte number in this protocol is little-endian** — the length, the CRC, the
  port, the status counter. One rule with no exceptions is the rule a second implementation
  in another language gets right.

### CRC-32

**CRC-32/ISO-HDLC**: reflected, polynomial `0xEDB88320`, init `0xFFFFFFFF`, final xor
`0xFFFFFFFF`. This is exactly what Go's standard library computes:

```go
crc := crc32.ChecksumIEEE(stream[:len(stream)-4])   // header + payload, not the CRC
```

Computed over `[version][payloadLen][payload]` — everything before the CRC field.

[TEST] Pinned on both ends by the published check value: `crc32("123456789") == 0xCBF43926`.
A checksum two languages must agree on is the single easiest thing in this protocol to get
subtly wrong, so it has its own test.

### The payload: TLV records

```
[tag:1][len:1][value: len bytes]   repeated, in any order
```

| tag | field | length | rule |
|---|---|---|---|
| `0x01` | SSID | 1..32 | **required**; empty is `FieldEmpty`, absent is `MissingSsid` |
| `0x02` | Wi-Fi password | 0..63 | **empty is legal — an open network**; 1..7 is refused |
| `0x03` | agent host | 0..63 | empty means "discover it over mDNS" |
| `0x04` | agent port | exactly 2 | little-endian, non-zero; absent means 8765 |
| `0x05` | device token | 0..64 | empty means "get one by pairing" |
| `0x06` | OTA password | 0..63 | empty leaves OTA disarmed |

The capacities are `usage::provision::kMax*` and are not arbitrary: the radio caps an SSID
at 32 bytes, and the Arduino core copies a WPA2 passphrase into a 64-byte field **without
terminating it** when the source is 64 bytes or longer, so 63 is the longest safely
terminated passphrase.

**Only the SSID is required, and that is a deliberate asymmetry.** `provision.h` draws a
line between `joinable()` (an SSID — worth attempting) and `complete()` (SSID + host +
token — reachable and authenticated). The BLE daemon normally sends everything, because it
knows everything; but the protocol accepts an SSID alone, because a device that joined and
has not yet been given a token is a legal, normal state, and requiring the token here would
re-create the failure this whole line of work exists to remove.

**A 1..7-character password is refused (`PassTooShort`)**, because the radio refuses it
outright: storing it guarantees a join failure the owner cannot diagnose. Empty is
different and is accepted — an open network is legal.

**Values are checked for control characters.** Any byte below `0x20`, or `0x7f`, in a
non-numeric value is `ControlChar`. This is not fussiness: `provision::sanitize()` *cuts a
field at* the first control byte, so a password containing one would be silently stored as
a different, shorter password. Bytes `0x80..0xFF` are allowed — an SSID is UTF-8 and "Café"
is a real network name.

### Forward compatibility

- Tags **below `0x80` are mandatory to understand**. An unknown one is `UnknownField`, not
  a shrug: a daemon sending a field this firmware has never heard of is describing a
  configuration the firmware cannot honour, and ignoring it would report success and behave
  wrongly.
- Tags **at or above `0x80` are skipped**. That is the escape hatch: a future daemon puts
  genuinely optional additions there and old firmware keeps provisioning.
- A duplicate tag is `DuplicateField` — last-wins would make the meaning of a stream depend
  on the order it happened to be written in.

### Sizes

- Largest legal stream: **306 bytes** — every field at capacity, plus header and CRC.
  [TEST] asserted, so the number cannot drift away from the code.
- Device buffer: **512 bytes** (`kMaxStream`), leaving room for optional tags. A BEGIN
  declaring more is `TooLarge`; the Info characteristic publishes the capacity so the
  daemon never has to guess.

---

## 6. Status and Info

### Status — 6 bytes, READ and NOTIFY

```
[version:1][state:1][error:1][nextSeq:1][received:2 LE]
```

`nextSeq` and `received` are there so a daemon that lost track can see exactly where the
device thinks it stands before deciding to start over.

| state | value | meaning |
|---|---|---|
| Idle | 0 | nothing in flight |
| Receiving | 1 | BEGIN accepted, chunks arriving |
| Ready | 2 | COMMIT validated; the record is parsed |
| Applying | 3 | the device is saving and joining |
| Applied | 4 | **stored and joined — provisioning succeeded** |
| Failed | 5 | see `error`; a fresh BEGIN is required |

**Ready is not success.** Only the hardware knows whether the record saved and the radio
joined, so the device answers `Applying` and then `Applied` or `Failed(JoinFailed)`. The
daemon must wait for `Applied` before telling the owner anything, and the dashboard should
show `Applying` as progress — the join takes seconds and a frozen dialog reads as a hang.

### Info — 12 bytes, READ, open

```
[version:1][flags:1][capacity:2 LE][mac:6][reserved:2]
```

`flags` bit 0 (`0x01`) means the device already holds a provisioned record. `reserved` is
sent as zero and must be ignored on read. Nothing here is secret — the MAC is in every
advertising packet already, and it is what lets the dashboard say *which* StickS3 it found.

---

## 7. Errors

The numbers are stable across firmware versions; the Go side switches on the number. Each
maps to a fixed sentence via `bleprov::errorText()` that can never contain a submitted
byte.

| # | name | text | what the daemon should do |
|---|---|---|---|
| 0 | None | ok | — |
| 1 | MalformedControl | bad command length | bug in the central; fix it |
| 2 | UnknownOp | unknown command | bug in the central |
| 3 | BadVersion | unsupported format version | the device is older than the daemon — offer a firmware update |
| 4 | ShortStream | declared size too small | bug in the central |
| 5 | TooLarge | too big for this device | read Info's capacity and shorten |
| 6 | NotBegun | no transfer in progress | send BEGIN |
| 7 | MalformedChunk | empty or malformed chunk | bug in the central |
| 8 | OutOfOrder | chunk out of order | restart with BEGIN |
| 9 | Overflow | more data than declared | bug in the central |
| 10 | Truncated | transfer incomplete | restart with BEGIN |
| 11 | LengthMismatch | length does not match | bug in the central |
| 12 | BadCrc | checksum mismatch | restart with BEGIN (retry once, then report) |
| 13 | MalformedTlv | malformed field | bug in the central |
| 14 | UnknownField | unknown field | the device is older than the daemon |
| 15 | DuplicateField | duplicate field | bug in the central |
| 16 | FieldEmpty | required field is empty | the SSID was blank — ask the owner |
| 17 | FieldTooLong | field too long | show which field, from the lengths it sent |
| 18 | ControlChar | illegal character in a field | the value came from a paste — ask the owner |
| 19 | PassTooShort | Wi-Fi password too short | ask the owner for a password of 8+ characters |
| 20 | BadPort | invalid port | bug in the central |
| 21 | MissingSsid | no network name | ask the owner |
| 22 | NotJoinable | nothing usable to join | ask the owner |
| 23 | SaveFailed | could not save credentials | device-side NVS failure — retry, then report |
| 24 | JoinFailed | could not join that network | **wrong password, or out of range** — ask the owner and retry |

`JoinFailed` is the one the owner will actually hit, so the dashboard must render it as a
sentence about their network, never as a code.

---

## 8. The happy path, in order

```
1.  scan for service 3e12c7ff-ef1e-4bcc-8b8c-0185d4474539
2.  connect
3.  read Info                          -> version, capacity, MAC
4.  (optional) negotiate a larger MTU
5.  subscribe to Status                -> THIS is what starts pairing
6.  owner types the 6 digits off the device screen; the link bonds
7.  build the stream                   -> version, payloadLen, TLVs, crc32
8.  write Control  BEGIN(1, totalLen)  -> Status: Receiving
9.  write Data     chunk 0..n-1        -> Status: Receiving, received climbs
10. write Control  COMMIT              -> Status: Ready, then Applying
11. wait on Status                     -> Applied  (success — tell the owner)
                                       -> Failed   (show errorText)
12. disconnect
```

**Step 5 is the pairing trigger, and that is deliberate.** Subscribing writes the CCCD of a
characteristic that requires an encrypted, MITM-protected link, so the stack must pair
before it can honour the write — the prompt appears exactly when the daemon is ready to
provision, not at connect time. The daemon should therefore treat step 5 as a
*long-running* operation gated on a human, not as a fast setup call, and it must surface
"look at the device screen" to the owner at that moment. On a device already bonded, step 5
returns immediately and step 6 disappears.

The device, on `Applied`: saves to NVS, `scrub()`s the record out of RAM, stops
advertising, tears down the BLE stack, and starts fetching.

### Worked example

[REAL] Produced by running the real encoder and decoder, not hand-written. Fabricated
values throughout — `HomeNet` / `correcthorse` / `192.168.0.42` / port 8765 / a 32-hex
token.

```
payloadLen = 75    totalLen = 82    crc32 = 0x7672F676

Control BEGIN:  01 01 52 00

stream:
0000  01 4B 00 01 07 48 6F 6D 65 4E 65 74 02 0C 63 6F
0016  72 72 65 63 74 68 6F 72 73 65 03 0C 31 39 32 2E
0032  31 36 38 2E 30 2E 34 32 04 02 3D 22 05 20 30 31
0048  32 33 34 35 36 37 38 39 61 62 63 64 65 66 30 31
0064  32 33 34 35 36 37 38 39 61 62 63 64 65 66 76 F6
0080  72 76

         ^^ 01        version
            4B 00     payloadLen = 75
            01 07 ..  tag 01 (SSID)  len 7   "HomeNet"
            02 0C ..  tag 02 (pass)  len 12  "correcthorse"
            03 0C ..  tag 03 (host)  len 12  "192.168.0.42"
            04 02 3D 22   tag 04 (port) len 2  0x223D = 8765
            05 20 ..  tag 05 (token) len 32
            76 F6 72 76   crc32, little-endian

Data chunks at MTU 23 (19 data bytes each):
  #0  00 01 4B 00 01 07 48 6F 6D 65 4E 65 74 02 0C 63 6F 72 72 65
  #1  01 63 74 68 6F 72 73 65 03 0C 31 39 32 2E 31 36 38 2E 30 2E
  #2  02 34 32 04 02 3D 22 05 20 30 31 32 33 34 35 36 37 38 39 61
  #3  03 62 63 64 65 66 30 31 32 33 34 35 36 37 38 39 61 62 63 64
  #4  04 65 66 76 F6 72 76

Control COMMIT: 02

Status after COMMIT:  01 02 00 05 52 00
                      ^^ version  ^^ Ready  ^^ None  ^^ nextSeq 5  ^^ received 82

Info (MAC 24:0A:C4:11:D5:34, unprovisioned):
  01 00 00 02 24 0A C4 11 D5 34 00 00
     ^^ flags  ^^^^^ capacity 512
```

---

## 9. What must still be measured on hardware

[UNVERIFIED] — every one of these is a documented expectation, which in this project is the
shape of a defect until someone puts it on the wire.

1. **Does macOS prompt for a device-displayed passkey against this peripheral?** The whole
   security design rests on it. If CoreBluetooth's prompt does not appear, or appears as
   Just Works, take the documented fallback.
2. **Does the bond survive?** The owner must type the passkey once per device, not once per
   session. Verify across a device reboot, a Mac reboot, and a daemon restart.
3. **Do `ESP_GATT_PERM_*_ENC_MITM` permissions actually refuse an unpaired write** on this
   core, or are they advisory? Test with an unpaired central; a write that succeeds means
   the design is not implemented.
4. **Coexistence with Wi-Fi.** The device joins Wi-Fi while the BLE link is still up to
   report `Applied` before it disconnects — BLE and Wi-Fi share one radio. Measure whether
   the notification lands, and whether the join slows down.
5. **The real cost, not the delta.** +578,928 B flash / +20,904 B RAM is the link-time
   figure. The runtime heap cost of a live BLE stack is a different number and must be
   measured the way the portal's ~53 KB was.
6. **The advertising window on battery.** The device sleeps behind a ~19 s wake window on
   battery; an unprovisioned device has nothing to save power for and should stay awake,
   but that interaction with the sleep policy has not been exercised.

---

## 10. Where this lives

| file | what it owns |
|---|---|
| `firmware/src/usage/bleprov.h` | the normative payload description, in C++ |
| `firmware/src/usage/bleprov.cpp` | the decoder, the CRC, the status/info encoders |
| `firmware/test/host/bleprov_fixture.h` | the daemon-side encoder, written from *this document* |
| `firmware/test/host/test_bleprov.cpp` | transport: the checksum, the control commands, the chunk stream |
| `firmware/test/host/test_bleprov_payload.cpp` | payload: TLV framing, field capacities, tag rules, the port |
| `firmware/src/usage/provision.h` | `Record`, `sanitize()`, `joinable()`, `complete()` — the storability rules this protocol defers to |
| `firmware/src/hal/sticks3/bleprov.*` | the GATT server, pairing, the screen (no parsing) |
| `firmware/src/hal/sticks3/creds.h` | `credsSave()` — where an accepted record goes |

The encoder in `bleprov_fixture.h` is deliberately a **second implementation**, written from
this document rather than by calling firmware code. A contract that cannot be implemented from
its own description shows up there as a failing test, instead of as a daemon that never
provisions anything.
