.PHONY: build install test clean

BINDIR := bin

build:
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/agentswarm-broker ./cmd/broker
	go build -o $(BINDIR)/agentswarm-server ./cmd/server
	go build -o $(BINDIR)/agentswarm ./cli

install: build
	cp $(BINDIR)/agentswarm-broker /usr/local/bin/
	cp $(BINDIR)/agentswarm-server /usr/local/bin/
	cp $(BINDIR)/agentswarm /usr/local/bin/

test:
	go test ./...

clean:
	rm -rf $(BINDIR)
