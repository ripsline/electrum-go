BINARY  := electrum-go
PKG     := ./cmd/server

VERSION    ?= $(shell git describe --tags --dirty 2>/dev/null || echo "0.1.0-dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X main.Version=$(VERSION) \
           -X main.GitCommit=$(GIT_COMMIT) \
           -X main.BuildTime=$(BUILD_TIME)

.PHONY: build vet test clean version

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

vet:
	go vet ./...

test:
	go test ./...

version:
	@echo "VERSION    = $(VERSION)"
	@echo "GIT_COMMIT = $(GIT_COMMIT)"
	@echo "BUILD_TIME = $(BUILD_TIME)"

clean:
	rm -f $(BINARY)
