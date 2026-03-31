# agentswarm

Real-time peer-to-peer communication for Claude Code sessions. Instant delivery, structured messaging, zero polling.

## Features

- **Instant delivery** — SSE push, not HTTP polling
- **Structured messages** — Types (question, alert, request), urgency, threading
- **Peer discovery** — See what other Claude sessions are working on
- **File conflict detection** — Warns when two peers edit the same file
- **Shared context** — Key-value store for API contracts, decisions, schemas
- **Broadcast** — Send to all peers in a repo, directory, or machine
- **Auto-everything** — Broker auto-launches, dead peers auto-clean

## Install

### One-liner (recommended)

```bash
go install github.com/scott-pallas/agentswarm/cmd/agentswarm-broker@latest
go install github.com/scott-pallas/agentswarm/cmd/agentswarm-server@latest
go install github.com/scott-pallas/agentswarm/cmd/agentswarm@latest
```

Binaries land in `~/go/bin/`. Make sure that's in your `$PATH`.

### From source

```bash
git clone https://github.com/scott-pallas/agentswarm.git
cd agentswarm
make build    # builds to ./bin/
make install  # copies to /usr/local/bin/
```

### Configure Claude Code

```bash
claude mcp add --scope user --transport stdio agentswarm -- agentswarm-server
```

The broker starts automatically when the first MCP server connects.

## Architecture

```
  ┌──────────────────────────────────┐
  │      BROKER (localhost:7899)     │
  │  SQLite │ HTTP API │ SSE Push    │
  └────┬─────────────────────┬───────┘
       │                     │
  HTTP POST             SSE (persistent)
       │                     │
  ┌────┴────┐          ┌─────┴────┐
  │ MCP Srv │          │ MCP Srv  │
  │ stdio ↕ │          │ stdio ↕  │
  │ Claude A│          │ Claude B │
  └─────────┘          └──────────┘
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `list_peers` | Discover other Claude Code instances |
| `send_message` | Send a message to a specific peer |
| `broadcast` | Send to all peers in a scope |
| `set_summary` | Describe what you're working on |
| `get_context` | Read shared context values |
| `set_context` | Set shared context values |
| `check_messages` | Manual message check (fallback) |

## CLI

```bash
agentswarm status              # Broker status
agentswarm peers               # List peers
agentswarm send <id> <msg>     # Send message
agentswarm broadcast <msg>     # Broadcast
agentswarm context list        # List context
agentswarm context get <key>   # Get context
agentswarm context set <k> <v> # Set context
agentswarm kill-broker         # Stop broker
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTSWARM_PORT` | `7899` | Broker port |
| `AGENTSWARM_DB` | `~/.agentswarm.db` | Database path |
| `AGENTSWARM_HEARTBEAT_MS` | `15000` | Heartbeat interval |
| `AGENTSWARM_STALE_TIMEOUT_MS` | `60000` | Stale peer timeout |

## Requirements

- Go 1.22+ (build only)
- Claude Code v2.1.80+ (channels support)

## License

MIT
