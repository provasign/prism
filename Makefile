.PHONY: build test lint install bootstrap clean

BIN := bin/prism

build:
	@mkdir -p bin
	go build -o $(BIN) ./cmd/prism

test:
	go test ./... -count=1

lint:
	go vet ./...
	gofmt -l internal cmd

install:
	go install ./cmd/prism

bootstrap:
	bash ./scripts/bootstrap-prism-grove.sh

clean:
	rm -rf bin dist
