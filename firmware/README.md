# StickS3 Firmware

PlatformIO project for the M5StickS3 (ESP32-S3-PICO-1-N8R8) running the usaged
display sketch.

## Prerequisites

- [PlatformIO Core](https://docs.platformio.org/en/latest//core-installation/index.html) (CLI)
- ESP32 toolchain (installed automatically by PlatformIO Core)

## Setup

```sh
cd firmware
cp include/secrets.h.example include/secrets.h   # then edit secrets.h
```

## Build

```sh
pio run
```

## Flash

```sh
pio run -t upload
```

## Monitor

```sh
pio device monitor
```

## Serial protocol

The firmware emits one structured line per serial log entry.  Each line
starts with a tag in brackets that identifies the event type.  This is how
hardware behaviour is verified without a camera or extra tools.

| Tag       | Format                                                                 |
|-----------|------------------------------------------------------------------------|
| `[BOOT]`  | `[BOOT] board=26 psram=8388608 build=abc123`                           |
| `[NET]`   | `[NET] state=connected ip=192.168.0.77`  (ip omitted when absent)      |
| `[FETCH]` | `[FETCH] code=304 rev=abcd1234 ms=120`  (304, no body — no seq)       |
| `[FETCH]` | `[FETCH] code=200 rev=abcd1234 seq=43 ms=310`  (200, new data)        |
| `[FETCH]` | `[FETCH] code=-1 err=timeout ms=8000`  (error; rev reused as err)     |
| `[RENDER]`| `[RENDER] page=1 lines=5 rev=abcd1234`                                 |
| `[ERR]`   | `[ERR] <what>`                                                        |

### Fetch variants

- **304 (Not Modified)** — the snapshot did not change, so `seq` is omitted.
  The device keeps its current page.
- **200 (OK)** — the snapshot changed; `seq` is the new sequence number and
  `rev` is the new revision.  The device redraws only if `rev` differs from
  the last render.
- **Error (code < 0)** — e.g. `code=-1 err=timeout`.  The `rev` parameter is
  reused as the error description and printed after `err=`.  No redraw occurs.
