# agentswarm

Real-time peer discovery and communication for multiple Claude Code sessions on the same machine.

## Features

- **Peer discovery** -- see what other Claude Code sessions are working on
- **Instant messaging** -- SSE push delivery, no polling
- **Orchestration** -- delegate tasks to other agents and wait for results
- **Spawn agents** -- launch new Claude Code sessions that auto-join the swarm
- **Shared context** -- key-value store for API contracts, decisions, schemas
- **Broadcast** -- send to all peers in a repo, directory, or machine
- **Auto-everything** -- broker auto-launches in-process, dead peers auto-clean, zero configuration

## Requirements

- **Go 1.24+** -- [install Go](https://go.dev/dl/)
- **Claude Code v2.1.80+** -- channels support required

## Install

### One-liner

```bash
go install github.com/scott-pallas/agentswarm/cmd/agentswarm-server@latest
```

Binary lands in `~/go/bin/`. Make sure that directory is in your `$PATH`.

### From source

```bash
git clone https://github.com/scott-pallas/agentswarm.git
cd agentswarm
make build    # builds to ./bin/
make install  # copies to /usr/local/bin/
```

## Configure Claude Code

```bash
claude mcp add --scope user --transport stdio agentswarm -- agentswarm-server
```

The broker starts automatically when the first MCP server connects. No separate daemon required.

## Architecture

```
  ┌──────────────────────────────────┐
  │      BROKER (localhost:7900)     │
  │  In-memory │ HTTP API │ SSE Push │
  │  (runs inside first MCP server) │
  └────┬─────────────────────┬───────┘
       │                     │
  HTTP POST             SSE (persistent)
       │                     │
  ┌────┴────┐          ┌─────┴────┐
  │ MCP Srv │          │ MCP Srv  │
  │ stdio   │          │ stdio    │
  │ Claude A│          │ Claude B │
  └─────────┘          └──────────┘
```

The first `agentswarm-server` process to start claims port 7900 and runs the broker in-process. Subsequent processes connect to it as clients. All state is held in memory -- no database files.

## MCP Tools

| Tool | Description |
|------|-------------|
| `list_peers` | Discover other Claude Code instances and what they are working on |
| `send_message` | Send a message to a specific peer (arrives instantly via SSE) |
| `broadcast` | Send a message to all peers in a scope (repo, directory, or machine) |
| `set_summary` | Describe what you are working on (visible to other peers) |
| `set_name` | Set a human-readable display name for this peer |
| `whoami` | Return your own peer ID, name, and registration info |
| `get_context` | Read a shared context value set by any peer in the same scope |
| `set_context` | Set a shared context value visible to all peers |
| `check_messages` | Manual message check (fallback -- messages normally arrive via push) |
| `spawn_agent` | Launch a new Claude Code agent that auto-joins the swarm |
| `wait_for_messages` | Block until one or more messages arrive |
| `delegate` | Spawn an agent with a task and get a task ID for tracking |
| `request_task` | Send a task request to an existing peer |
| `report_result` | Report the result of a completed task back to the requester |
| `wait_for_result` | Block until a delegated task completes and return its result |
| `list_tasks` | List all active tasks (delegated, received, or both) |
| `cancel_task` | Cancel a pending or in-progress task |

## Orchestration

The `delegate` and `wait_for_result` tools enable structured task orchestration:

```
Orchestrator                    Worker (spawned automatically)
     │                                │
     │  delegate("refactor auth")     │
     │──────────────────────────────►│
     │  returns task_id               │
     │                                │
     │  ... continues other work ...  │
     │                                │
     │  wait_for_result(task_id)      │
     │◄──────────────────────────────│
     │  returns result                │
```

1. Call `delegate` with a prompt -- this spawns a new agent and returns a `task_id` immediately.
2. Continue working on other things while the agent executes.
3. Call `wait_for_result` with the `task_id` to block until the agent finishes and retrieve its result.

For tasks assigned to existing peers rather than new agents, use `request_task` and `report_result`.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTSWARM_PORT` | `7900` | Broker HTTP port |
| `AGENTSWARM_HEARTBEAT_MS` | `15000` | Heartbeat interval in milliseconds |

## License

MIT
