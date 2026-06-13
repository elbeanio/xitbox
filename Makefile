# xb Makefile
#
# Build the xb CLI and xit-guardian proxy daemon.
# Both binaries land in ./bin/ — see `make where`.

BIN_DIR       := bin
XB_BIN        := $(BIN_DIR)/xb
GUARDIAN_BIN  := $(BIN_DIR)/xit-guardian

GO            ?= go
GOFLAGS       ?=
INSTALL_DIR   ?= $(HOME)/go/bin

.PHONY: all build xb xit-guardian test vet check fmt tidy clean install uninstall where help

all: build

## build: build both binaries (default target)
build: xb xit-guardian

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

## xb: build the xb CLI to ./bin/xb (static binary for relay inside bwrap)
xb: $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(XB_BIN) ./cmd/xb

## xit-guardian: build the proxy daemon to ./bin/xit-guardian
xit-guardian: $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $(GUARDIAN_BIN) ./cmd/xit-guardian

## test: run all tests
test:
	$(GO) test ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## check: vet + test
check: vet test

## fmt: gofmt all files
fmt:
	$(GO) fmt ./...

## tidy: go mod tidy
tidy:
	$(GO) mod tidy

## clean: remove ./bin/
clean:
	rm -rf $(BIN_DIR)

## install: install both binaries to $(INSTALL_DIR)
install: build
	install -m 0755 $(XB_BIN)      $(INSTALL_DIR)/
	install -m 0755 $(GUARDIAN_BIN) $(INSTALL_DIR)/

## uninstall: remove both binaries from $(INSTALL_DIR)
uninstall:
	rm -f $(INSTALL_DIR)/xb $(INSTALL_DIR)/xit-guardian

## where: print absolute paths to the built binaries
where:
	@echo "xb:            $(CURDIR)/$(XB_BIN)"
	@echo "xit-guardian:  $(CURDIR)/$(GUARDIAN_BIN)"

## help: list targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //'
