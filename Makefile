BINARY  := kelper
PKG     := ./cmd/kelper
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# k9s is embedded and stamps its own version banner, so its build vars are set
# alongside kelper's. -s -w strips debug info, which offsets the size k9s adds.
K9SPKG  := github.com/derailed/k9s/cmd
K9SVER  ?= $(shell go list -m -f '{{.Version}}' github.com/derailed/k9s 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.appVersion=$(VERSION) \
           -X $(K9SPKG).version=$(K9SVER) \
           -X $(K9SPKG).commit=$(VERSION) \
           -X $(K9SPKG).date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PREFIX  ?= /usr/local

.PHONY: build test install clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

test:
	go test ./...

install: build
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)

clean:
	rm -f $(BINARY)
