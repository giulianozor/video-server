BINARY   := videosrv
PREFIX   ?= /usr/local
BINDIR   := $(PREFIX)/bin

.PHONY: all build test install clean

all: build

build:
	go build -o $(BINARY) .

test:
	go test -race -v ./...

install: build
	sudo install -d $(BINDIR)
	sudo install -m 0755 $(BINARY) $(BINDIR)/$(BINARY)
