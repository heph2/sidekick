BIN := bin/sidekick

.PHONY: all build test fmt vet clean

all: fmt vet test build

build:
	go build -o $(BIN) ./cmd/sidekick

test:
	go test ./...

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

clean:
	rm -rf bin
