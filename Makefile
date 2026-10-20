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

# kelper-fetch is injected into pods by `kelper pull`, so it is built for every
# supported node architecture, independent of the GOOS/GOARCH kelper itself
# targets, and embedded from internal/pull/assets.
FETCH_ARCHES := amd64 arm64
FETCH_DIR    := internal/pull/assets
FETCHERS     := $(foreach a,$(FETCH_ARCHES),$(FETCH_DIR)/kelper-fetch-linux-$(a))
FETCH_SRC    := $(shell find cmd/kelper-fetch internal/pull/fetchproto -name '*.go' -not -name '*_test.go') go.mod go.sum

.PHONY: build fetchers test test-sh install clean

build: $(FETCHERS)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

fetchers: $(FETCHERS)

$(FETCH_DIR)/kelper-fetch-linux-%: $(FETCH_SRC)
	CGO_ENABLED=0 GOOS=linux GOARCH=$* go build -trimpath -ldflags "-s -w" -o $@ ./cmd/kelper-fetch

test:
	go test ./...

# Offline tests of the bash edition; needs jq.
test-sh:
	bash test/kelper_sh/test_kelper_sh.sh

install: build
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)

clean:
	rm -f $(BINARY) $(FETCHERS)
