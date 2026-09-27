BINARY := dbq
GO ?= go
PREFIX ?= $(HOME)/.local

.PHONY: build test lint fmt fmt-check vet install clean cleanup

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

# remove build artifacts only
clean:
	rm -rf bin

# remove artifacts AND the binary installed by 'make install'
# (does not touch Homebrew-managed installs - use 'brew uninstall dbq')
cleanup: clean
	@if [ -f "$(PREFIX)/bin/$(BINARY)" ]; then \
		rm -f "$(PREFIX)/bin/$(BINARY)"; \
		echo "removed $(PREFIX)/bin/$(BINARY)"; \
	else \
		echo "$(PREFIX)/bin/$(BINARY) not installed"; \
	fi
