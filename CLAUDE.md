# CLAUDE.md -- agentswarm

## What is this?

agentswarm enables multiple Claude Code sessions to discover each other and communicate in real-time on the same machine. It uses SSE (Server-Sent Events) for instant message delivery instead of polling.

## Architecture

- **MCP Server** (`cmd/agentswarm-server/`) -- Single binary. Runs as an MCP stdio server for one Claude Code session. The first instance to start also launches an in-process HTTP broker on localhost:7900. Subsequent instances connect to the existing broker as clients.
- There is no standalone broker binary and no CLI binary.
- All state is held in memory via `internal/broker/store.go`. Nothing is written to disk.

## Building

```bash
make build    # Builds bin/agentswarm-server
make install  # Copies to /usr/local/bin/
make test     # Run tests
make clean    # Remove bin/
```

## Key Files

| File | Purpose |
|------|---------|
| `internal/types/types.go` | All shared types (Peer, Message, Task, etc.) |
| `internal/broker/store.go` | In-memory store (peers, messages, tasks, context) |
| `internal/broker/sse.go` | SSE connection manager |
| `internal/broker/broker.go` | HTTP routes + handlers (including task endpoints) |
| `internal/server/context.go` | Git/CWD/TTY detection |
| `internal/server/stream.go` | SSE client |
| `internal/server/mcp.go` | MCP tools + handlers |
| `internal/server/spawn.go` | Agent spawning + prompt building |
| `cmd/agentswarm-server/main.go` | Entry point, signal handling, broker URL setup |

## Dependencies

- `github.com/mark3labs/mcp-go` -- MCP SDK for Go

## Testing

Run `go test ./...` for unit tests. For integration testing, start two MCP server sessions and have them communicate.

## Conventions

- Go 1.24
- Standard library for HTTP, no frameworks
- JSON over HTTP for broker API
- SSE for real-time push
- `internal/` for non-exported packages
- Orchestration via delegate/wait_for_result tools
