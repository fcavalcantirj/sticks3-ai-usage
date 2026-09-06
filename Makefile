VERSION ?= dev
PIO_ENV ?= m5stack-sticks3

.PHONY: build fmt vet lint test verify clean smoke install uninstall fw-test fw-build verify-all fw-ota fw-build-ota fw-upload-ota

build:
	@commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none); \
	mkdir -p bin; \
	go build -ldflags "-X main.version=$(VERSION) -X main.commit=$$commit" -o bin/usaged ./cmd/usaged

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

verify: fmt vet lint test build

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

fw-ota:
	@# Combined build+upload for cable sessions (device stays powered on USB).
	@# For battery sessions: make fw-build-ota, wake device, make fw-upload-ota BIN=...
	@bash firmware/scripts/upload_ota.sh

fw-build-ota:
	@bash firmware/scripts/upload_ota.sh --build-only

fw-upload-ota:
	@test -n "$(BIN)" || { echo "usage: make fw-upload-ota BIN=path/to/firmware.bin [BUILD_ID=abc123]"; exit 1; }
	@bash firmware/scripts/upload_ota.sh --upload-only "$(BIN)" "$(BUILD_ID)"

verify-all: verify fw-test fw-build
