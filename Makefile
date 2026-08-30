.PHONY: run dev test test-v cover lint arch tidy build clean

run:            ## start the gateway once
	go run ./cmd/gateway

dev:            ## start with live reload (air)
	go tool air

test:
	go test ./...

test-v:
	go test ./... -v

cover:          ## per-package coverage summary
	go test ./... -coverprofile=tmp/cover.out
	go tool cover -func=tmp/cover.out | tail -1

lint:
	go vet ./...
	@test -z "$$(gofmt -l . | grep -v '^tmp/')" || (echo "gofmt needed:"; gofmt -l .; exit 1)

# arch fails the build if the domain depends on an adapter — the one
# hexagonal rule, machine-checked so it cannot rot.
arch:
	@! go list -f '{{join .Imports "\n"}}' ./internal/core/... \
		| grep -q 'internal/adapter' \
		|| (echo "ARCH VIOLATION: internal/core imports an adapter"; exit 1)
	@echo "arch ok: core depends on no adapter"

tidy:
	go mod tidy

build:
	go build -o tmp/gateway ./cmd/gateway

clean:
	rm -rf tmp
