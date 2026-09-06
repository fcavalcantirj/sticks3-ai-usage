---
round: 1
builder_session: fresh planner+builder session taking over the provisioning group; verified the handover against the live repo, the running agent, and the running M5Burner install on 2026-09-06
---

# Plan r1 — Task 75, the provisioning spike

## Blocking constraints, restated

1. **A passing test proves nothing across a process or hardware boundary.** Tests run on my
   assumptions; the wire runs on reality. `addHeader("User-Agent")` is silently dropped by
   the Arduino core, `esp_timer_get_time()` does not survive deep sleep, a TCP probe was
   aimed at UDP 3232, and M5Unified never enables charging — four for four, each green in
   tests. So for every spike question I record a *measured number obtained from the running
   device or a real build*, never an expectation.
2. **Never hand-start `usaged` on port 8765.** A hand-started process takes the port,
   launchd then cannot bind, nothing survives a reboot, and every later log reading is
   worthless — this manufactured a fictional "the device never fetches" defect. Host
   changes go live only via `make build && launchctl kickstart -k
   gui/$(id -u)/com.fcavalcanti.usaged`, confirmed with `lsof -nP -iTCP:8765 -sTCP:LISTEN`
   showing launchd's pid.
3. **An empty log is not evidence until I know who writes that log.** Silence has to be
   attributed before it can be interpreted. `scripts/device-report.sh` is the instrument —
   it prints the device's claimed age beside the server's authoritative `server_age_s` and
   the `drift_s`, and refuses to report success when there is nothing to compare.
4. **Redraw-only-on-change is the invariant the product rests on.** The snapshot body is
   hashed into `rev`; anything that varies per request must live outside that hash, as
   `age` and `device_state` already do, or the device redraws forever. A 304 stays a 304
   with an empty body.
5. **Every device upload needs Felipe's explicit authorization** (also AGENTS.md house rule
   6). Standing permission covers the upload only — never powering the device on. And a
   task whose verify step names Felipe does not flip to `passes: true` until he says so.

---

## Handover discrepancies

Checked every `[REAL]` claim I could reach. Most were exact. Seven mismatches:

1. **The credential leak is five values, not four.** The handover's "Where things stand"
   bullet lists WIFI_SSID, WIFI_PASS, USAGED_DEVICE_TOKEN, USAGED_HOST. Re-run against
   `firmware/.pio/build/m5stack-sticks3/firmware.bin`, **`OTA_PASS` also appears verbatim**.
   All five match. (`spec.json` tasks 75/76 already say five — only the handover
   undercounts.) This changes task 76's security check, which must cover five values.
2. **HEAD is `7c59ad1`**, not `c1d18a0` — the handover commit itself landed after it was
   written. Tree clean, branch `main`. Harmless.
3. **The build path in the verbatim command block is wrong from the repo root.** It reads
   `.pio/build/m5stack-sticks3/firmware.bin`; the real path is
   `firmware/.pio/build/m5stack-sticks3/firmware.bin`. `.pio/` does not exist at the root.
4. **Binary size differs**: handover says 1,051,312 bytes, on-disk build is **1,051,328**.
   A different build. Both are ~31.4% of the `0x330000` app partition (`default_8MB.csv`) —
   the headroom claim stands, the number does not.
5. **`GOLDEN_RULES.md` #8, "NO SOLO RESEARCH", is not mentioned anywhere in the handover**
   and bears directly on a task that is by definition investigative. Also #1 (TDD, 80%
   coverage) sits against task 75's own instruction that spike code is throwaway. Both
   handled explicitly below rather than silently.
6. **`spec.json` tasks carry no `id` field** — the objects are
   `{category, description, steps, passes}` and "task 75" is purely positional (index 74).
   Anyone grepping for an id finds nothing.
7. **`BACKLOG.md` is off by one** — it calls pairing "task 77" and Wi-Fi-under-Settings
   "task 78"; `spec.json` has them at 78 and 79. Also `AGENTS.md`'s Status block still says
   "55 tasks, 43 passed" against the real 84/78. Cosmetic; I will not rewrite text I did not
   author.

Verified clean, for the record:

- Route list in `internal/api/server.go:91-104` matches the handover exactly, all 13.
- The Preferences pattern task 76 is told to reuse is real: `board.cpp:96-136`, namespace
  `"usaged"`, `loadRotation`/`saveRotation` plus a second validated setting.
- `WiFiProv`, `BLE`, `SimpleBLE`, `DNSServer`, `WebServer`, `ESPmDNS` all ship in the
  Arduino core in use; no `build_flags` disable BLE.
- Agent healthy under launchd, **pid 39603 owns `:8765`**.
- Device asleep as described — `ping 192.168.0.136` 100% loss, no `/dev/cu.usbmodem*`.
- `net.cpp:9-16` carries **two `#error` guards** that hard-require `secrets.h`. Task 76 must
  remove them; the handover never names this concrete edit.
- `firmware/CMakeLists.txt` globs `src/usage/*.cpp`, so a new pure module for task 76 is
  picked up by `make fw-test` automatically.

---

## Q4 is already answered — done during verification, at zero cost

The handover's `next_action` was "answer Q4 first, it is the cheapest and the only one that
could delete work". It cost nothing: **M5Burner is installed at
`/Applications/M5Burner.app`**, it is an Electron app, and its `app.asar` is readable. This
is inspection of a local install, not the web deep-dive Golden Rule 8 forbids.

**[REAL] — read from `/Applications/M5Burner.app/Contents/Resources/app.asar`, 2026-09-06.**

### Am I reading the code Felipe actually runs? Yes — checked.

Felipe's window titles itself `v202605221800` while the bundle says `3.0.0`, so I checked
rather than assumed:

- `ps aux` shows the running process is **`/Applications/M5Burner.app/Contents/MacOS/m5burner`**
  — the exact bundle I read.
- `app.js` explains the two version numbers: the Electron **main process** (which does all
  flashing and plugin dispatch — the code below) ships in `app.asar` at **3.0.0**, while the
  **UI shell** is downloaded and self-updated from
  `http://m5burner-cdn.m5stack.com/appVersion.info` → `patch/<version>.zip` into `view/`,
  and pushed to the renderer as `mainWin.webContents.send('get-version', updateInfo.version)`.
  That downloaded shell is `v202605221800`, which is why none of its UI strings
  ("Refresh Firmwares", "Only Official") appear in the bundle.

So the flashing logic I read **is** the flashing logic he runs. The caveat is closed.

### What M5Burner actually does

Two credential-injection mechanisms exist, and neither one rescues us.

**(a) Per-product NVS mixins — hardcoded, not for us.** Exactly four:
`mixinUIFlow2NVS`, `mixinOpenaiNVS`, `mixinOpenaiCamNVS`, `mixinStamplcNVS`. Each generates
an ESP-IDF `nvs_partition_gen` CSV and mixes the result into the flash image. Only two
namespaces are ever used — `config` (keys `wifi_ssid`, `wifi_password`, plus product extras
like `openaikey`, `language`) and `uiflow` (keys `ssid0`/`pswd0`, `server`, `tz`, …). Each is
bound to one first-party M5Stack product.

**(b) A generic Wi-Fi plugin — real, but not NVS and not reachable by us.** Dispatch is
data-driven on `opts.payload.pluginType`:

```js
if(opts.payload.pluginType === PluginTypes.WIFI) {
  pluginConfig = useWifiPacker({...args, address: flashAddr})
}
```

`pluginAddr` comes from the firmware's catalog entry, with `'ADDR_END'` resolving to
`binary length + 0x1000`. `useWifiPacker` writes a **100-byte raw blob, not NVS**:

```
buf = 100 bytes filled 0xff
buf[0]                    = ssid length
buf[1 .. 1+len-1]         = ssid bytes
buf[1+len]                = ssid checksum   (sum of bytes & 0xff)
buf[50]                   = password length
buf[51 .. 51+len-1]       = password bytes
buf[51+len]               = password checksum
```

(so SSID and password are each capped at 48 bytes by the 50-byte halves.)

### Verdict: NO SHORTCUT. Tasks 77 and 79 survive.

Three independent reasons, any one of them sufficient:

1. `pluginType` arrives on the **catalog entry**, which M5Stack controls. **Felipe, who has
   published to M5Burner, confirms he saw no such option in the publishing flow** — only a
   description field. A community listing cannot declare it.
2. Even if it could, the blob carries **SSID and password only**. It can never carry the
   agent host or the device token — which is exactly the "decisive constraint" task 75
   states about every off-the-shelf provisioning framework. That constraint is now
   *measured for M5Burner specifically* rather than assumed.
3. `ADDR_END` is fragile by construction: it moves with every build and could land inside
   `app1` or `spiffs` in our `default_8MB.csv` layout.

**Consequence:** Q4 deletes nothing and costs no further work. **Task 76 will NOT build a
blob reader.** The format goes into `docs/DEVICES.md` so it is never rediscovered, and task
76 gains one line recording that it was evaluated and declined. Felipe reacted with surprise
rather than choosing; this is my call and he can overrule it — building the reader would
save a published user one field out of four while adding a reserved flash address to every
build, for a path a community listing cannot even reach.

---

## Acceptance checklist — point-by-point

| # | Requirement | How this plan satisfies it |
|---|---|---|
| 1 | Restate all five blocking constraints | "Blocking constraints, restated" above, in my own words, each with the failure it prevents. |
| 2 | Task 75 first; no code for 76-79 until Q1-Q5 are answered with measured numbers | Phases A-C are task 75 only. **I commit to writing no implementation code for 76-79 until Phase C is written and Felipe has flipped 75.** The only code this plan produces is throwaway spike code, deleted in C3, never entering `src/`. |
| 3 | Per question, what is measured and how it is recorded | The Phase B table gives every question a measurement, an instrument, and a pre-declared threshold. All five land in one `docs/DEVICES.md` section with numbers. |
| 4 | Q4 scheduled early | **Already done** — answered before this plan was written, exactly as `next_action` asked, including confirming I read the running build. It deleted nothing; the reason is now measured rather than assumed. |
| 5 | How the redraw-only-on-change trap is avoided | Task 75 touches no host code at all, so it cannot break the invariant. The design commitment carried into task 79 is recorded in C2: a **separate** device-directed endpoint, no ETag, never hashed into `rev`, acknowledged so a change applies once — never a new snapshot field. `age`/`device_state` are the precedent. |
| 6 | Build and flash without assuming the device is awake; powering on is Felipe's | Phase A needs no device — `pio run` alone answers Q1's fit half. Phase B opens with an explicit authorization request; per Felipe's answer the device will be **on USB**, where it never sleeps, so there is no ~19 s window. If that changes, `flash_when_awake.sh` builds first and uploads on his button press. **Powering the device on is his action and I never assume it.** |
| 7 | Credential-leak check as an explicit deliverable | Named as a task 76 deliverable and **corrected to five values** (discrepancy 1): a build with no `secrets.h` whose `firmware.bin` contains none of WIFI_SSID, WIFI_PASS, USAGED_HOST, USAGED_DEVICE_TOKEN, **OTA_PASS**. C2 amends task 76's step text to say five. Not built here — task 75 does not implement. |
| 8 | Does not start task 83, reintroduce the IMU, bind BtnA hold, or move the token check back into `config.Load` | None of the four appear anywhere in this plan. Task 83 stays deferred, the accel path stays deleted, BtnA hold stays reserved and dead, and the non-loopback token check stays in `api.New`. |
| 9 | How host changes are verified live | No host change in task 75. When one comes (78/79): `make build && launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.usaged`, then `lsof -nP -iTCP:8765 -sTCP:LISTEN` must show launchd's pid, then hit the running endpoint — never a green test against a stale binary. |

---

## The plan

### Phase A — desk work, no device

**A1. Record Q4.** Add a `### Provisioning spike (task 75)` section to `docs/DEVICES.md`
with the finding above: both mechanisms, the 100-byte layout, the three reasons it is not a
shortcut, the main-process/UI version split, and Felipe's publishing-side confirmation.

**A2. M5Burner version caveat — ALREADY RESOLVED** (see Q4 above). The running process is
the bundle I read; `v202605221800` is the separately-downloaded UI shell.

**A3. Q1, the fit half — no hardware needed.** Add a throwaway `[env:spike]` to
`firmware/platformio.ini` with `build_src_filter` pointing at `firmware/spike/`
(**outside `src/`**, per task 75's "do not let it become the implementation"). It brings up
`WiFi.softAP` + `WebServer` + `DNSServer`. Then `pio run -e spike` and read the flash/RAM
lines.

Baseline measured today: **1,051,328 bytes, 31.4% of the `0x330000` app partition**; RAM
16.6% per the handover. Deltas recorded as absolute bytes and percent.

### Phase B — hardware, device on USB (needs Felipe)

**B0.** Ask for explicit authorization for this specific OTA upload (AGENTS.md #6), and for
the device to be on the cable. On USB it never sleeps (`vbusPresent`), serial gives
`[BOOT]`/`[NET]`/`[HEAP]` directly, and the cable is the guaranteed recovery path.

**Spike firmware safety design — the part that must not be got wrong:**

1. Join home Wi-Fi in STA and **arm `ArduinoOTA` first**, before any AP work, so the device
   is always re-flashable. `otaBegin()` re-arms on every connect (the ptt.ino lineage — a
   once-ever guard is the bug that killed OTA after sleep).
2. **Never deep-sleep.** A portal that sleeps is not a portal.
3. Print every measurement over serial *and* on screen, so a number survives a dropped
   serial session.
4. Recovery ladder: OTA → USB cable at `/dev/cu.usbmodem101` → nothing else needed.

**B1-B5, the measurements:**

| Q | Measured | Instrument | Pre-declared threshold |
|---|---|---|---|
| Q1 run | Free and min heap with SoftAP + WebServer + DNSServer all live | `[HEAP] free= min=` over 5 min | Against today's `free=281168 min=275316`. A min-heap floor under ~80 KB reshapes 77. |
| Q2 | 30 `scanNetworks()` calls in `WIFI_AP_STA` with a phone associated: empty-result count, networks found, whether the phone stays associated | Serial count per scan + the phone's own Wi-Fi status | **>10% empty results OR any client disassociation = flaky** → 77 switches to scan-before-AP with a cached list plus manual entry |
| Q3 | Does the captive sheet auto-pop, per platform | Felipe's actual iPhone, plus an Android if one is to hand | Pop / no-pop recorded separately per platform. No-pop on iOS → on-screen "open your browser and go to …" becomes 77's **primary** path, not a fallback |
| Q5 | AP→STA: live switch vs reboot; and whether an NVS write immediately before survives it | `WiFi.mode()` transition attempt, then the reboot path; write a sentinel key, read it back after | Reboot is acceptable and simpler; the point is to *confirm* rather than assume, including that credentials written just before are there after |

**B6. Restore production firmware over OTA** and confirm with `sh scripts/device-report.sh`
that the device's claimed age and `server_age_s` agree — silence is not a pass.

### Phase C — record, amend, delete

**C1.** Append all five answers, with numbers, to the `docs/DEVICES.md` section from A1.

**C2.** Amend `spec.json` tasks 76-79 by **appending** an `AMENDED BY SPIKE (2026-09-06)`
step to each rather than rewriting steps I did not author. Each amendment says plainly which
assumption survived and which did not. Task 76's also carries the five-value correction from
discrepancy 1; task 79's restates the separate-endpoint/no-ETag/never-touches-`rev` design.

**C3.** Delete `firmware/spike/` and the `[env:spike]` block. Confirm `git status` is clean
of spike artefacts and that `make fw-build` still produces the production binary.

**C4.** Report to Felipe. **Task 75 flips to `passes: true` only when he says so.**

### Files touched

- `docs/DEVICES.md` — new spike section (the only permanent artefact).
- `spec.json` — appended amendment steps on 76-79; `passes` on 75 only on Felipe's word.
- `firmware/platformio.ini` + `firmware/spike/` — throwaway, deleted in C3.
- Nothing in `firmware/src/`, nothing in `internal/`, no host code.

### Verification

- **Phase A:** `pio run -e spike` prints flash/RAM; deltas against the measured
  1,051,328 B / 31.4% baseline. No device required.
- **Phase B:** every number read off the wire — serial `[HEAP]` and scan counts, the phone's
  own behaviour for Q3, a sentinel NVS key read back after reboot for Q5.
- **Phase B6:** `sh scripts/device-report.sh` must show the device's age against
  `server_age_s` with a sane `drift_s`. It refuses to report success without a comparison,
  so a silent log cannot be mistaken for a pass.
- **Phase C3:** `make fw-build` SUCCESS and `make fw-test` Failed 0 with the spike removed,
  proving the throwaway left nothing behind.
- **No host code changes in this task**, so no `make build` + kickstart cycle is needed — and
  if that changes, the listener gets checked with `lsof` before any conclusion is drawn.

---

## Concerns

- **[HIGH] Golden Rule 8 vs the nature of a spike.** #8 forbids self-directed investigation.
  Task 75 is investigation by definition — but it is committed in the ledger and therefore
  sanctioned, so I treat the ledger entry as the approval. Q4's one locally-unanswerable
  half (the publishing side) I did **not** research: Felipe answered it from his own
  publishing experience. I will not widen beyond the five questions.
- **[HIGH] Flashing the spike removes the working usage monitor for the session.** Restore is
  one OTA (B6). The real risk is a spike that breaks OTA, which is why OTA is armed before
  any AP work and why the USB cable is the standing fallback.
- **[MEDIUM] Q3 is not automatable and depends on Felipe's phone.** It needs him present, and
  an Android may not be available — in which case Android stays `[UNVERIFIED]` and is
  labelled as such rather than guessed.
- **[MEDIUM] Q2's threshold is a judgment call.** Pre-declared at >10% empty or any client
  drop, so the verdict is mechanical instead of retrofitted to whatever I observe. See
  question 1.
- **[MEDIUM] Golden Rule 1 (TDD, 80% coverage) does not apply to spike code**, which task 75
  explicitly calls throwaway. Stating it rather than letting it look like an omission.
- **[LOW] `BACKLOG.md` off-by-one and the stale `AGENTS.md` Status block.** Reported, not
  fixed — not my text.

---

## Questions for the author

1. **Q2 threshold** — do you accept ">10% empty scans OR any client disassociation = flaky,
   fall back to scan-before-AP"? Pre-declaring it is what stops the verdict from being fitted
   to the data afterwards.
2. **`spec.json` amendment style** — appending an `AMENDED BY SPIKE` step to each of 76-79,
   rather than editing existing step text, is my reading of "do not rewrite task text you did
   not author". Confirm that is what you want.
3. **The OTA_PASS leak** — it makes task 76's security check five values, not four. Any
   reason it was left out, or is it simply an undercount in the handover?
