VERSION ?= dev

.PHONY: build fmt vet lint test verify clean smoke install uninstall fw-test fw-build verify-all

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

verify-all: verify fw-test fw-build
