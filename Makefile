BIN := solar
PREFIX ?= $(HOME)/.local
VERSION ?= $(shell v=$$(git describe --tags --abbrev=0 2>/dev/null); echo $${v:-dev} | sed "s/^v//")
LDFLAGS := -s -w -X github.com/wetakush/solaragent/internal/app.Version=$(VERSION)

.PHONY: build install test vet clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)
	@echo "☀ installed $(PREFIX)/bin/$(BIN)"

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN)
