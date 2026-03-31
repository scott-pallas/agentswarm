# CLAUDE.md — agentswarm

## What is this?

agentswarm enables multiple Claude Code sessions to discover each other and communicate in real-time on the same machine. It uses SSE (Server-Sent Events) for instant message delivery instead of polling.

## Architecture

- **Broker** (`cmd/broker/`) — Singleton HTTP+SSE daemon on localhost:7899 with SQLite storage
- **MCP Server** (`cmd/server/`) — One per Claude Code session, stdio transport, connects to broker
- **CLI** (`cli/`) — Debugging/management utility

## Building

```bash
make build    # Builds bin/agentswarm-broker, bin/agentswarm-server, bin/agentswarm
make install  # Copies to /usr/local/bin/
make test     # Run tests
make clean    # Remove bin/
```

## Key Files

| File | Purpose |
|------|---------|
| `internal/types/types.go` | All shared types |
| `internal/broker/db.go` | SQLite schema + queries |
| `internal/broker/sse.go` | SSE connection manager |
| `internal/broker/broker.go` | HTTP routes + handlers |
| `internal/server/context.go` | Git/CWD/TTY detection |
| `internal/server/stream.go` | SSE client |
| `internal/server/mcp.go` | MCP tools + handlers |

## Dependencies

- `github.com/mark3labs/mcp-go` — MCP SDK for Go
- `modernc.org/sqlite` — Pure Go SQLite (no CGo)

## Testing

Run `go test ./...` for unit tests. For integration testing, start the broker and two MCP server sessions.

## Conventions

- Go 1.22+ (uses `http.NewServeMux` with method+pattern routing)
- Standard library for HTTP, no frameworks
- JSON over HTTP for broker API
- SSE for real-time push
- `internal/` for non-exported packages
