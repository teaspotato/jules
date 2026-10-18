VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test lint

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o jules ./cmd/jules

install:
	go install -trimpath -ldflags "-s -w -X main.version=$(VERSION)" ./cmd/jules

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
