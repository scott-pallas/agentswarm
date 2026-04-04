.PHONY: build install test clean

BINDIR := bin

build:
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/agentswarm-server ./cmd/agentswarm-server

install: build
	@mkdir -p $(HOME)/.local/bin
	cp $(BINDIR)/agentswarm-server $(HOME)/.local/bin/
	@echo "Installed to $(HOME)/.local/bin/agentswarm-server"
	@echo "Make sure $(HOME)/.local/bin is in your PATH"

test:
	go test ./...

clean:
	rm -rf $(BINDIR)
