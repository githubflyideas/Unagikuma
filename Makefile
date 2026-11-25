GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet fmt check dist clean fuzz

all: fmt vet test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/unagikuma ./cmd/unagikuma

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "run gofmt -w ." && exit 1)

# Fuzz every parser of untrusted input for FUZZTIME each.
FUZZTIME ?= 30s
FUZZ := internal/config:FuzzConfig internal/config:FuzzSelector internal/cluster:FuzzReadFrame \
	internal/cluster:FuzzDecodeJoin internal/cluster:FuzzCompatible internal/probe:FuzzParseDNS \
	internal/probe:FuzzBER internal/probe:FuzzJSONPath internal/probe:FuzzUDPReplies \
	internal/schedule:FuzzCron internal/schedule:FuzzEvery internal/importer:FuzzKuma internal/backup:FuzzRestore \
	internal/config:FuzzWriteBack internal/config:FuzzUpsertRoundTrip internal/config:FuzzRoundTrip internal/importer:FuzzNagios
fuzz:
	@for f in $(FUZZ); do pkg=$${f%%:*}; fn=$${f##*:}; echo "== $$fn"; \
	  $(GO) test -run='^$$' -fuzz="^$$fn$$" -fuzztime=$(FUZZTIME) ./$$pkg || exit 1; done

# Release archives for all platforms (see scripts/dist.sh).
dist:
	scripts/dist.sh $(VERSION)

clean:
	rm -rf bin dist
