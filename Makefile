# arw2uhdr build
BIN     := bin/arw2uhdr
GO      ?= go
export CGO_ENABLED=1

.PHONY: all build test vet clean deps

all: build

build:
	@mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/arw2uhdr
	@echo built $(BIN)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	gofmt -l . | (! grep .) || (echo "gofmt needed on files above" && false)

deps:
	@echo "Debian/Ubuntu: sudo apt install libraw-dev pkg-config"
	@echo "macOS:         brew install libraw pkg-config"

clean:
	rm -rf bin
