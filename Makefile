GO ?= go
MODULES := server clients/cli
VERSION ?= dev

.PHONY: all test race fmt fmt-check vet lint build run dist clean

all: fmt-check vet test build

# `go test ./...` no funciona desde la raíz: es un workspace multi-módulo y la
# raíz no contiene ningún módulo. Hay que iterar por módulo.
test:
	@set -e; for m in $(MODULES); do echo "==> test $$m"; $(GO) -C $$m test ./...; done

race:
	@set -e; for m in $(MODULES); do echo "==> test -race $$m"; $(GO) -C $$m test -race ./...; done

fmt:
	$(GO) fmt ./server/... ./clients/cli/...

fmt-check:
	@out="$$(gofmt -l server clients)"; \
	if [ -n "$$out" ]; then echo "gofmt pendiente en:"; echo "$$out"; exit 1; fi

vet:
	@set -e; for m in $(MODULES); do echo "==> vet $$m"; $(GO) -C $$m vet ./...; done

lint: fmt-check vet

build:
	$(GO) -C server build -o ../bin/server ./cmd/server
	$(GO) -C clients/cli build -ldflags "-X main.version=$(VERSION)" -o ../../bin/cli .

# binarios para las plataformas soportadas
dist:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64 $(GO) -C server build -o ../dist/server_linux_amd64 ./cmd/server
	GOOS=windows GOARCH=amd64 $(GO) -C server build -o ../dist/server_windows_amd64.exe ./cmd/server
	GOOS=darwin  GOARCH=arm64 $(GO) -C server build -o ../dist/server_darwin_arm64 ./cmd/server
	GOOS=linux   GOARCH=amd64 $(GO) -C clients/cli build -ldflags "-X main.version=$(VERSION)" -o ../../dist/cli_linux_amd64 .
	GOOS=windows GOARCH=amd64 $(GO) -C clients/cli build -ldflags "-X main.version=$(VERSION)" -o ../../dist/cli_windows_amd64.exe .
	GOOS=darwin  GOARCH=arm64 $(GO) -C clients/cli build -ldflags "-X main.version=$(VERSION)" -o ../../dist/cli_darwin_arm64 .

run:
	$(GO) -C server run ./cmd/server

clean:
	# sólo bin/: dist/ contiene binarios versionados
	rm -rf bin
