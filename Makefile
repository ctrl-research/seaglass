BINARY  := seaglass
MODULE  := github.com/ctrl-research/seaglass
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test test-integration lint tidy clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

run: build
	./bin/$(BINARY) $(ARGS)

test:
	go test ./... -race -count=1

# Runs against a real cluster. Example: make test-integration CONTEXT=kind-seaglass-dev
test-integration:
	SEAGLASS_TEST_CONTEXT=$(CONTEXT) go test -tags integration ./internal/k8s/ -run TestStreamLive -v -count=1

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

.PHONY: lint-install
lint-install:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
