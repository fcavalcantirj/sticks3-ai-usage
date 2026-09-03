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
