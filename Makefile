BINARY  = stremio-cliuwu
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed "s/^v//" || echo dev)

.PHONY: run build build-all build-linux build-windows install clean

run:
	go run .

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) .
	@echo "built → ./$(BINARY)"

build-all: build-linux build-windows

build-linux:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o dist/$(BINARY)-linux .

build-windows:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o dist/$(BINARY)-windows.exe .

install: build
	install -m755 $(BINARY) /usr/local/bin/$(BINARY)
	@echo "installed → /usr/local/bin/$(BINARY)"

clean:
	rm -f $(BINARY)
	rm -rf dist/
