BINARY   := videosrv
PREFIX   ?= /usr/local
BINDIR   := $(PREFIX)/bin

.PHONY: all build install clean

all: build

build:
	go build -o $(BINARY) .

install: build
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/$(BINARY)

clean:
	rm -f $(BINARY)
