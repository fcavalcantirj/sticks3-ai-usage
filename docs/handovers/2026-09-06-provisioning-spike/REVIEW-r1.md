---
verdict: APPROVED
round: 1
---

# Review r1 — Task 75 provisioning spike

**APPROVED.** All nine acceptance items satisfied, several exceeded. The builder is now the
primary session.

I re-verified every discrepancy it raised rather than taking them on trust. **All seven are
correct, and four are defects in my handover.** That is the outcome this loop exists to
produce, and it is worth more than a plan that agreed with me.

## Acceptance checklist — verdict per item

| # | Requirement | Verdict |
|---|---|---|
| 1 | Restate the five blocking constraints | **Satisfied.** In its own words, each paired with the failure it prevents rather than parroted. |
| 2 | Task 75 first; no code for 76-79 until Q1-Q5 answered | **Satisfied.** Explicit commitment, and spike code is confined to `firmware/spike/` outside `src/`, deleted in C3. |
| 3 | Per question, what is measured and how recorded | **Satisfied, and exceeded.** The Phase B table gives each question an instrument *and a pre-declared threshold*. Declaring the pass line before seeing data is the part I did not ask for and should have. |
| 4 | Q4 scheduled early | **Exceeded.** Already answered, at zero cost, before the plan was written. |
| 5 | Redraw-only-on-change trap | **Satisfied.** Correctly observes task 75 touches no host code, and carries the design commitment forward to 79: separate endpoint, no ETag, never hashed into `rev`, acknowledged so a change applies once. |
| 6 | Build/flash without assuming the device is awake; powering on is Felipe's | **Satisfied.** Phase A needs no device; Phase B opens with an authorization request. |
| 7 | Credential-leak check as an explicit deliverable | **Satisfied, and it corrected me** — five values, not four. |
| 8 | No task 83, no IMU, no BtnA hold, no token check back in `config.Load` | **Satisfied.** All four named and excluded. |
| 9 | How host changes are verified live | **Satisfied.** Rebuild, kickstart, confirm the listener is launchd's pid. |

## Discrepancies — my errors, confirmed

1. **OTA_PASS is in the binary: five values, not four.** Confirmed by re-running `strings`
   against `firmware/.pio/build/m5stack-sticks3/firmware.bin`. Worse than an omission — I
   *had* verified OTA_PASS during the session and then wrote four in the handover. Task 76's
   check covers five. **This is the most valuable catch in the plan**: an undercounted
   security check is one that passes while leaking.
2. **HEAD is `7c59ad1`**, not `c1d18a0` — the handover commit landed after the file was
   written. Harmless, correct.
3. **The `strings` path is wrong from the repo root.** `.pio/` does not exist there; it is
   `firmware/.pio/`. Confirmed. Anyone pasting my command gets an empty result and could
   read that as "no leak" — the exact absence-of-evidence trap in blocking constraint 3,
   which I wrote and then set up.
4. **Binary size** 1,051,328 on disk vs my 1,051,312. Different build; the headroom claim
   stands, the number does not.
5. **`GOLDEN_RULES.md` #8 "NO SOLO RESEARCH" is absent from my handover.** Confirmed at
   line 19, and it bears directly on an investigative task. A real gap.
6. **`spec.json` has no `id` field** — task numbers are purely positional. True.
7. **`BACKLOG.md` is off by one.** Confirmed: it still says "tasks 75-78" and calls pairing
   77 and Settings-Wi-Fi 78, from before I inserted the spike at 75 and shifted them to
   76-79. My error; I am fixing it now since it is my text.

## Answers to the builder's questions

1. **Q2 threshold — accepted as written.** ">10% empty scans OR any client disassociation =
   flaky, fall back to scan-before-AP with a cached list plus manual entry." Pre-declaring
   it is correct and is the right instinct: a threshold chosen after seeing the data is not
   a threshold. If the result lands near the line, report the raw counts and say it is
   marginal rather than rounding to a verdict.
2. **`spec.json` amendment style — confirmed, append only.** An `AMENDED BY SPIKE
   (2026-09-06)` step on each of 76-79 is exactly the right reading. Do not edit step text
   you did not author; the ledger's value is that it records what was believed *at the
   time*, including where it was wrong.
3. **OTA_PASS — a plain undercount on my part, no reasoning behind it.** I verified all five
   during the session and wrote four. Correct the check to five and treat the handover as
   wrong wherever it disagrees with what you measure.

## Ruling on Golden Rule 8

Your reading is right and I am making it explicit so it is not re-litigated. Reading a
locally installed application's bundle to answer a question the ledger *explicitly poses*
is executing the task, not expanding scope — the committed task is the approval #8 asks
for. And you drew the line in the correct place: the half that could not be answered
locally, whether M5Burner's publishing flow exposes `pluginType`, you did not go
researching — you asked Felipe, who answered from his own experience. That is exactly the
behaviour #8 is protecting.

The M5Burner call itself — not building a blob reader — is also right, and for the reason
you gave rather than the effort: a 100-byte blob carrying only SSID and password cannot
convey the agent host or the device token, so it does not solve the problem even where it
is reachable. That is the "decisive constraint" now measured for M5Burner specifically
instead of assumed. Recording the format in `docs/DEVICES.md` so it is never rediscovered
is the right disposition.

## Handover corrections applied

`HANDOVER.md` gets a `## SUPERSEDED` banner rather than a rewrite (the skill's rule, and
the same principle as append-only ledger amendments). `BACKLOG.md`'s numbering I am fixing
directly, as it is my text and simply wrong.

## next_action

Phase A1 — write the Q4 finding into `docs/DEVICES.md`. You are the primary session from
here; report to Felipe directly.

Two things to carry, neither of them in the checklist:

- The `net.cpp:9-16` `#error` guards that hard-require `secrets.h` are a concrete edit task
  76 needs and my spec never named. Good catch — fold it into the C2 amendment.
- Task 75 flips to `passes: true` only on Felipe's word. Its verify step names him.
