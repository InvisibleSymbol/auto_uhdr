# arw2uhdr build
BIN     := bin/arw2uhdr
GO      ?= go
export CGO_ENABLED=1

.PHONY: all build tools test vet fmt check clean deps

all: build

build:
	@mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/arw2uhdr
	@echo built $(BIN)

# Debug/dev commands under tools/.
tools:
	@mkdir -p bin
	$(GO) build -o bin/ ./tools/...
	@echo built tools into bin/

test:
	$(GO) test ./...

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...
	@gofmt -l . | (! grep .) || (echo "gofmt needed on the files above" && false)

# Everything CI enforces.
check: vet test

deps:
	@echo "Debian/Ubuntu: sudo apt install libraw-dev pkg-config"
	@echo "macOS:         brew install libraw pkg-config"

clean:
	rm -rf bin
