VERSION ?= dev

.PHONY: build fmt vet lint test verify clean smoke

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
	test -x $(HOME)/go/bin/staticcheck && $(HOME)/go/bin/staticcheck ./... || echo 'staticcheck missing, skipped'

test:
	go test -count=1 ./...

verify: fmt vet lint test build

clean:
	rm -rf bin/

smoke: build
	@scripts/smoke.sh
