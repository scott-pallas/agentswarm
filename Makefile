.PHONY: build install test clean

BINDIR := bin

build:
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/agentswarm-broker ./cmd/agentswarm-broker
	go build -o $(BINDIR)/agentswarm-server ./cmd/agentswarm-server
	go build -o $(BINDIR)/agentswarm ./cmd/agentswarm

install: build
	cp $(BINDIR)/agentswarm-broker /usr/local/bin/
	cp $(BINDIR)/agentswarm-server /usr/local/bin/
	cp $(BINDIR)/agentswarm /usr/local/bin/

test:
	go test ./...

clean:
	rm -rf $(BINDIR)
