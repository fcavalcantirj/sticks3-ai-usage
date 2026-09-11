VERSION ?= dev
PIO_ENV ?= m5stack-sticks3

.PHONY: fw-auto build dist fmt vet lint test test-js test-statusline test-install verify clean smoke install uninstall fw-test fw-build fw-check-secrets verify-all fw-ota fw-build-ota fw-upload-ota

build:
	@commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none); \
	mkdir -p bin; \
	go build -ldflags "-X main.version=$(VERSION) -X main.commit=$$commit" -o bin/ai-usage ./cmd/usaged

dist:
	@# Distributable macOS release: universal binary + installer + checksums.
	@# Tag first, so VERSION comes from git describe: make dist VERSION=v0.1.0
	@bash scripts/dist.sh

fmt:
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "gofmt found unformatted files:"; \
		gofmt -l .; \
		exit 1; \
	fi

vet:
	go vet ./...

lint:
	@scripts/lint.sh

sync-prices:
	@bash scripts/sync-prices.sh

test:
	go test -count=1 ./...

verify: fmt vet lint test test-js test-statusline test-install build

test-install:
	@# v0.3.0 minted a 64-char OTA password against a 63-byte wire limit, so no
	@# device could be set up over Bluetooth at all. Nothing measured it.
	@bash scripts/install-release.test.sh

test-statusline:
	@# The status line is a shipped surface. It reported the daemon as down for
	@# days after the usaged->ai-usage rename because nothing executed it.
	@bash scripts/statusline.test.sh

test-js:
	@if ! command -v node >/dev/null 2>&1; then \
		echo "node not installed; skipping JS tests"; \
	else \
		node --test scripts/dashboard.test.mjs; \
	fi

clean:
	rm -rf bin/

smoke: build
	@scripts/smoke.sh

install: build
	@scripts/install.sh

uninstall:
	@scripts/uninstall.sh

fw-test:
	cmake -S firmware -B firmware/build && cmake --build firmware/build && ./firmware/build/usage_tests

fw-build:
	cd firmware && pio run

fw-publish-check:
	@# The gate that matters: build with NO secrets.h, then prove the image
	@# carries none of the five values.  A developer build legitimately
	@# contains them (secrets.h is the first-boot NVS seed), so checking THAT
	@# build always fails and tells you nothing.
	@bash firmware/scripts/publish_check.sh

fw-check-secrets:
	@# Fails if any secrets.h credential value is still inside the binary.
	@# Optional: make fw-check-secrets BIN=path/to/firmware.bin
	@sh firmware/scripts/check_no_secrets.sh "$(BIN)"

fw-ota:
	@# Build, then upload the moment the device is awake. Works on battery:
	@# press a button when prompted. Address comes from the daemon, never cached.
	@sh firmware/scripts/ota_auto.sh

fw-build-ota:
	@bash firmware/scripts/upload_ota.sh --build-only

fw-upload-ota:
	@test -n "$(BIN)" || { echo "usage: make fw-upload-ota BIN=path/to/firmware.bin [BUILD_ID=abc123]"; exit 1; }
	@bash firmware/scripts/upload_ota.sh --upload-only "$(BIN)" "$(BUILD_ID)"

verify-all: verify fw-test fw-build
