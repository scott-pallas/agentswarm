.PHONY: build install test clean

BINDIR := bin

build:
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/agentswarm-server ./cmd/agentswarm-server

install: build
	cp $(BINDIR)/agentswarm-server /usr/local/bin/

test:
	go test ./...

clean:
	rm -rf $(BINDIR)
