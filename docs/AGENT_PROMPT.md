# Set up ai-usage with an AI agent

Paste everything in the box below into Claude Code, Codex CLI, Cursor, or any
agent that can run shell commands on your Mac. It will install the daemon, set
up your StickS3, and check its own work.

You do not need to read the rest of this file. It is the prompt itself, written
so the agent knows what to do *and* what this project has learned the hard way.

---

```
You are setting up "ai-usage" on this Mac for me.

WHAT IT IS
A small Go daemon that reads my Claude Code and Codex CLI usage quotas and
serves them to a local dashboard and, optionally, to an M5StickS3 — a tiny
screen that sits on my desk showing how much of my 5-hour and weekly limits I
have left. Source: https://github.com/fcavalcantirj/sticks3-ai-usage

HOW IT GETS MY NUMBERS — read this before you tell me anything is broken.
It never asks for a password and cannot log in as me. It READS credentials that
other tools already store:
  - Claude: the macOS Keychain item "Claude Code-credentials", owned by Claude Code
  - Codex:  ~/.codex/auth.json, owned by the Codex CLI
So if I have not installed and logged into those tools, the matching provider
will correctly show "run claude" or "run codex". That is not a bug, and it is
not something you should try to fix by finding a token somewhere else. Tell me
which tools I am missing and stop.
OpenRouter and Groq are optional and take API keys I can add in Settings later.

YOUR RULES FOR THIS JOB
1. NEVER ask me for, echo, log, or store my Wi-Fi password or any API key. The
   daemon reads the Wi-Fi password from my keychain by itself and hands it to
   the device over an encrypted Bluetooth link. If you find yourself about to
   print a credential, stop.
2. VERIFY EVERY STEP AGAINST THE RUNNING SYSTEM, not against what a command
   printed. This project has a long history of things that reported success and
   were broken on the wire. After each step, check the actual state.
3. Do not modify my shell config, my .env files, or anything in ~/.claude or
   ~/.codex.
4. If something fails, show me the exact error and what you checked. Do not
   guess and retry blindly.

STEP 1 — INSTALL
Run:
  curl -fsSL https://raw.githubusercontent.com/fcavalcantirj/sticks3-ai-usage/main/install.sh | bash

Prefer building from source instead? That works too and needs Go 1.26+:
  git clone https://github.com/fcavalcantirj/sticks3-ai-usage
  cd sticks3-ai-usage && make install

VERIFY (do not skip):
  curl -s http://127.0.0.1:8765/healthz
Expect JSON with "ok":true. Then confirm the listener belongs to launchd, not
to a stray process you or I started:
  lsof -nP -iTCP:8765 -sTCP:LISTEN
  launchctl print gui/$(id -u)/com.fcavalcanti.ai-usage | grep -E 'state|pid'
The pid from launchctl must match the one holding the port. If it does not,
something is running by hand and will vanish on reboot — tell me.

STEP 2 — SHOW ME THE DASHBOARD
Open http://127.0.0.1:8765 and tell me what each provider says. Read it from
the API so you are describing reality:
  curl -s http://127.0.0.1:8765/v1/usage | head -c 800
A provider showing "auth" with "run claude" / "run codex" means I have not
logged into that tool. Say so plainly.

STEP 3 — THE STICKS3 (skip if I do not have one)
First ask me: is the stick powered on, and did I flash it from M5Burner?
It must be running the "ai-usage" firmware. If I flashed it more than ten
minutes ago, ask me to unplug and replug it — see the timing note below.

Then:
  curl -s -X POST http://127.0.0.1:8765/v1/setup/scan
  sleep 14
  curl -s http://127.0.0.1:8765/v1/setup

Look at the "scan" and "offer" fields. If a device was found, provision it:
  curl -s -X POST -H 'Content-Type: application/json' \
    -d '{"addr":"<the addr from offer>"}' \
    http://127.0.0.1:8765/v1/setup/provision
Then poll GET /v1/setup every few seconds and narrate the "run" steps to me.

WHEN IT REACHES THE "pairing" STEP, TELL ME TO LOOK AT THE STICK. macOS will
ask for six digits and they are displayed on the device's own screen. That is
the only thing I type in this entire process. Do not try to find those digits
yourself — they exist only on the screen, deliberately, because typing them is
how the system knows I am physically holding the device.

Success is run.state == "applied". Anything else, show me run.error and
run.next verbatim — those sentences are chosen by the daemon and are accurate.

VERIFY IT ACTUALLY WORKED. "applied" means the device said it joined. Confirm
it is really talking to us:
  tail -20 ~/Library/Logs/ai-usage/ai-usage.err.log | grep '"peer"'
You are looking for requests to /v1/usage from an IP that is not 127.0.0.1 —
that is the stick fetching. If none appear within a couple of minutes, the
setup reported success but the device is not reaching the daemon; tell me.

THINGS THAT GO WRONG, AND WHAT THEY ACTUALLY MEAN
- "No new StickS3 nearby" — usually the Bluetooth window closed. The stick
  advertises for ten minutes after boot, then gives up and raises its own Wi-Fi
  network instead (its screen will show "SETUP - join this wi-fi"). Ask me to
  power-cycle the stick, then scan again. Bluetooth does not come back without
  a restart: the firmware releases the radio's memory permanently to give it
  back to the rest of the system.
- Setup routes return 401 — you are not calling from this Mac. Requests from
  localhost need no token; anything else does.
- The device joins Wi-Fi but never fetches — check that the daemon is bound to
  the LAN and not just loopback: `curl -s http://127.0.0.1:8765/v1/config |
  grep listen`. It must not be 127.0.0.1, or the stick can never reach it.
- Gatekeeper blocks the binary — only happens on a browser download. The binary
  is not notarized (that needs a paid Apple certificate this project does not
  have). The curl one-liner avoids it entirely.
- "port is busy" when flashing — something is holding the serial port, usually
  a serial monitor. `lsof /dev/cu.usbmodem*` will name it.

WHEN YOU ARE DONE
Tell me: which providers are live, whether the stick is set up, and anything
you could not verify. Be honest about the last one — I would rather know what
you did not check than be told everything is fine.
```

---

## Why a prompt rather than a script

A script does one thing and fails opaquely. An agent can read the actual error,
check the running system, and tell you which of the four common causes it hit —
and, importantly, tell you when it *cannot* verify something.

The prompt is deliberately opinionated about verification because every serious
defect in this project's history looked like success at the layer above. The
daemon reporting "Done" while the device sat in its fallback setup screen is a
real thing that happened, not a hypothetical.
