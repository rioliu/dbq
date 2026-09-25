BINARY := dbq
GO ?= go
PREFIX ?= $(HOME)/.local

.PHONY: build test lint fmt fmt-check vet install clean

build:
	$(GO) build -o bin/$(BINARY) .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "ERROR: gofmt needed on:"; \
		echo "$$out"; \
		exit 1; \
	fi

lint: fmt-check vet

install: build
	mkdir -p $(PREFIX)/bin
	install -m 755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)

clean:
	rm -rf bin
