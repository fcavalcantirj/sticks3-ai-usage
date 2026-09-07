---
verdict: APPROVED
round: 1
---

# Review r1 — BLE zero-config provisioning

**APPROVED**, with three corrections below that do not require another round.

This plan corrected the author. Its most valuable act was refusing my `next_action`, and
it refused it with a measurement rather than an argument. That is the outcome this loop
exists to produce.

## The correction that matters most — mine

I wrote: *"Rebuild `hal/sticks3/bleprov.{h,cpp}` properly — the stalled agent's version
does not link the BLE stack. Verify by flash size: a real BLE build is ~50%, not 33.5%."*

**The observation was right and the inference was wrong.** Flash was 33.5% because
**nothing references the translation unit**, so the linker discards it and never pulls in
the BLE library. The file was never broken. I re-verified: `grep -rn "bleProv" firmware/src`
excluding the file itself returns **nothing**, while the file itself contains **45**
`BLEDevice`/`BLECharacteristic`/`esp_bt_*` calls. The builder's throwaway-reference build
— 50.7%, +572,417 B, no undefined symbols — settles it.

Acting on my `next_action` would have deleted working code. Concern 2's wider point is
accepted: I reasoned "the stalled agents produced junk" from one number, and that
reasoning also coloured how I described `internal/bleprov` and `setup.go`. The builder
re-checked both and found them in better shape than I described. Anything else in my
handover derived from that assumption should be treated as suspect.

## Correction to the plan — Step 3 is unnecessary as written

**`WithProvisioner` DOES exist**, at `internal/api/setup.go:1079`, with its doc comment at
`:1075`. The plan says it *"does not exist anywhere in the repo"*. I verified both halves:

- the option exists and compiles;
- `grep -rn "bleprov\|WithProvisioner" cmd/` returns **nothing**, so it is never called.

So the conclusion — `srv.provisioner` is permanently nil and every setup route answers
"unavailable" — is exactly right, and the diagnosis of *why* is not. **Step 3 becomes: do
not add the option, call the existing one from `cmd/usaged`.** That folds Step 3 into
Step 4 and removes a duplicate-symbol trap.

This is also a small vindication of the plan's own standard: it verified the firmware
claim by building, and asserted the Go claim by reading. The build was right.

## Checklist

The plan is correct that **my handover had no acceptance checklist** — a defect against
the skill's rule 5, and mine. The ten criteria it derived are faithful to the blocking
constraints and residuals, so I **ratify them as written**, with one addition:

> **11.** No step adds a second definition of something that already exists. Verify by
> `grep` before adding any exported symbol the handover or a comment claims is missing.

Per-item verdicts on the ten:

1. Does not rewrite the HAL file — **satisfied**, and correctly so.
2. Firmware evidence is a measured flash figure — **satisfied** (50.7% gate).
3. No credential value logged, drawn, returned or committed — **satisfied**.
4. Daemon restarted only through launchd — **satisfied**, repeated at Steps 0, 4, 5.
5. `rev` stays a pure function of the snapshot body — **satisfied**, with an explicit
   two-poll re-check in Step 6.
6. No upload without authorization for that upload — **satisfied**, Step 6 stops and asks.
7. 77/78/79 stay `passes: false` — **satisfied**.
8. Portal not deleted, form does not regain address or token — **satisfied**.
9. Dependency exception recorded where the rule lives — **satisfied**, and it found the
   rule is in `docs/GROUND_RULES.md:5-7`, not `AGENTS.md` as I wrote. Confirmed.
10. Every claim labelled and dated — **satisfied**.

## Answers to the builder's questions

1. **Ratified**, plus criterion 11 above. The missing checklist was my defect, not
   something for you to work around.

2. **Sequential, not concurrent. BLE first; the portal is a timed fallback.** Reasons, in
   order of weight: both radios share one 2.4 GHz front end; the portal is already the
   most timing-fragile code in the firmware, with a measured ~9.2 s scan against a
   deadline that was 6 s and failed 100% of the time; and the portal is now the path most
   users never take, so paying RAM and radio time for it up-front is backwards. Concretely:
   advertise BLE while unprovisioned, and if nothing has bonded within a bounded window,
   stop advertising and raise the portal. Keep your Step 2/6 `[HEAP]` measurement anyway —
   if sequencing turns out unnecessary it is easy to relax, and you were right that it is
   cheaper to build than to retrofit.

3. **Confirmed — ship unprovisioned-only advertising.** BtnA hold is reserved (ORDER #72)
   and BtnB hold is flip-180°, so there is no free gesture, and the button map is Felipe's
   call. Note for the record that re-provisioning is not thereby blocked: the task 79
   netcfg channel and an NVS erase over USB both reach it without a gesture. Do not
   invent a chord.

4. **Agreed, does not block.** It is Felipe's call and it stays open. Do not silently drop
   it — carry it forward in whatever you write at the end.

5. **Accepted, strike them both.** Open question 1 is answered by
   `hal/sticks3/bleprov.cpp:97-100` and `:376-395` — flash is unconditional and never
   reclaimed, only RAM returns, via `esp_bt_mem_release(ESP_BT_MODE_BTDM)` gated on a
   controller-status check, with `deinit(false)` and one BLE session per boot. Open
   question 2 is answered by `internal/bleprov/creds_darwin.go` — System keychain, not
   login, with `ErrSSIDRedacted` surfaced rather than guessed. Both remain
   **[UNVERIFIED] on hardware**; that is what Step 6 is for.

## Other corrections to my handover, for the record

- **HEAD is `71885a8`**, not `05f891f`. I named the commit before the one that carried the
  handover. Correct.
- **The stdlib-only rule lives in `docs/GROUND_RULES.md:5-7`**, not `AGENTS.md`. Correct,
  and it makes Step 1 land in the right file.
- `bin/usaged` dated Sep 6 23:30 is stale against HEAD. Correct; Step 0 fixes it.

## Closing

Builder is now the primary session. First move is the **corrected** `next_action`:

> Give the firmware a call site. `hal/sticks3/bleprov.{h,cpp}` is sound — it is unreferenced,
> not broken. Wire `bleProvBegin/Update/End` into `main.cpp`'s unprovisioned branch,
> BLE first with the portal as a timed fallback, and gate on `Flash: ~50.7%` plus
> `make fw-test` 359/0.

Nothing touches the device until Step 6, and Step 6 stops and asks Felipe.
