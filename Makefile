.PHONY: build test lint install bootstrap clean

# Build and test against the Grove version go.mod pins, not a sibling
# checkout picked up through a parent go.work (REL-02). To work against a
# local Grove, run with GOWORK= (empty: Go finds the workspace) and
# PRISM_ALLOW_LOCAL_GROVE=1 for the version test.
export GOWORK ?= off

BIN := bin/prism
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/provasign/prism/internal/version.Version=$(VERSION)

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/prism

test:
	go test ./... -count=1

lint:
	go vet ./...
	gofmt -l internal cmd

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/prism

bootstrap:
	bash ./scripts/bootstrap-prism-grove.sh

clean:
	rm -rf bin dist
