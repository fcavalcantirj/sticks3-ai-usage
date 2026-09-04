# usaged

`usaged` is a lean Go service that polls AI-usage APIs (Claude, Codex, OpenRouter,
Groq), persists a canonical snapshot, and serves it to a local web page and an
M5StickS3 status monitor. The founder owns all credentials; the code reads them
from the macOS Keychain and `~/.codex/auth.json` and never refreshes or copies them.

## Architecture

```
          ┌────────────┐         ┌──────────┐          ┌──────────┐
          │  Claude    │  token  │          │  JSON v1  │          │
          │  Code      │ from KC │  usaged  │────────│ snapshot │
          └────────────┘         │  (Go)     │ ETag/304│  state   │
          ┌────────────┐  token  │           │          │          │
          │  Codex     │ from jc │          │          │  file    │
          └────────────┘         └────┬─────┘          └──────────┘
                                      │ HTTP
                                      │   LAN 0.0.0.0:8765
                       ┌──────────────┼──────────────┐
                       │  browser     │  M5StickS3   │
                       │  dashboard   │  (OTA, unit #2)│
                       └──────────────┴──────────────┘
```

## Quick start

```sh
cp .env.example .env          # edit USAGED_DEVICE_TOKEN and provider keys
make install                  # builds, validates .env, installs + starts LaunchAgent
open http://127.0.0.1:8765/   # or LAN: http://<mac-ip>:8765/?token=<your-token>
```

Or run without installing:

```sh
make build && ./bin/usaged serve
# LAN clients: http://<mac-ip>:8765/?token=<your-token>
```

## CLI

| Command | Description |
|---|---|
| `usaged version` | Print version + commit. |
| `usaged serve` | Start the poller (900 s) and HTTP API server. |
| `usaged once [--json]` | Single fetch cycle; prints table or JSON. Exit 0 if all ok/stale, 3 if any auth/error/off. |
| `usaged once --fixtures DIR` | Offline mode: serve from captured fixtures. |
| `usaged once --scenario NAME` | Apply a scenario overlay from `testdata/scenarios/NAME/`. |

## API

| Endpoint | Method | Auth | Description |
|---|---|---|---|
| `/healthz` | GET | none (loopback) | `{ok, seq, rev, checked_at, uptime_sec}`. |
| `/v1/usage` | GET | token (LAN only) | Snapshot JSON v1 with `ETag: "<rev>"`. `If-None-Match` match → `304` (empty body). |
| `/v1/usage.txt` | GET | token (LAN only) | Human table (same as `usaged once`). |
| `/v1/refresh` | POST | token (LAN only) | Trigger an immediate poll; `200` if refreshed, `202` if coalescing. |
| `/` `/index.html` | GET | none (loopback) | Embedded dashboard. |

The dashboard polls `/v1/usage` every 60 s with `If-None-Match`; the body is
empty on 304 so mobile data is never re-downloaded.

## Snapshot v1 contract

```json
{"v":1,"seq":1,"rev":"a1b2c3d4","generated_at":1788414949,
 "checked_at":1788414949,"next_sec":900,
 "providers":[
   {"id":"claude","label":"Claude","plan":"max_20x","status":"ok",
    "msg":"","fetched_at":0,
    "rows":[{"k":"5h","label":"CLAUDE 5h","pct":19,"txt":"05:09",
             "tier":"ok","reset_at":1788411000}]}]}
```

`rev` is an 8-hex SHA-256 of the provider/row data. `seq` increments only when
`rev` changes. The firmware stores `rev` in `lastRev` and skips redraw on 304.

## Firmware

```sh
cd firmware
pio run                                   # build
pio run -e m5stack-sticks3 -t upload      # serial flash (unit #2 only)
make fw-ota                               # OTA flash (unit #2 only, MAC-guarded)
pio device monitor                        # serial monitor @ 115200
make fw-test                              # host-unit tests
```

**Unit #2 only.** The device is MAC `14:c1:9f:d4:d5:34`, hostname
`sticks3-usage`. Unit #1 (`ac:27:6e:d2:68:b8`) is off-limits. OTA is
password-protected via `OTA_PASS` in `secrets.h` (never committed).

Serial protocol (one line per event, see `firmware/README.md` for the full table):

```
[BOOT] board=26 psram=8388608 build=abc123 fw=1.0.0
[WAKE] cause=power_on vbus=5282
[NET] state=connected ip=192.168.0.136
[FETCH] code=200 rev=a1b2c3d4 seq=1 ms=310
[RENDER] page=1 lines=5 rev=a1b2c3d4
[HEAP] free=8388608 min=6543210
[SLEEP] reason=battery
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| `auth` badge / 401 from Claude | Run `claude` in a terminal to re-authenticate; the Keychain item `Claude Code-credentials` is read automatically. |
| Keychain prompt denied | Re-run `make install` with Felipe present; click **Always Allow** when the system prompts. |
| `429` from API | Cooldown ~24 h; the scheduler honors `Retry-After` and backs off. |
| Stale badge / `Mac asleep` | Wake the Mac; Wi-Fi sleep pauses polling. |
| OTA upload refused | Check MAC guard in `firmware/scripts/upload_ota.sh`; verify `OTA_PASS` in `secrets.h`. |
