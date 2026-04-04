# agentswarm -- Technical Specification

> Real-time peer-to-peer communication for Claude Code sessions.
> Instant delivery, structured messaging, zero polling.

**Author:** Scott Pallas
**Version:** 0.1.0
**Date:** 2026-04-01
**Language:** Go
**Runtime:** Single binary (no runtime dependencies)
**Protocol:** MCP (Model Context Protocol)
**Repo:** github.com/scott-pallas/agentswarm

---

## Overview

agentswarm enables multiple Claude Code sessions to discover each other and communicate in real-time on the same machine. It replaces HTTP polling with Server-Sent Events (SSE) for instant message delivery, and provides structured message types, broadcast messaging, a shared key-value context store, agent spawning, and task-based orchestration.

### Design Principles

1. **Instant, not polled** -- SSE push for zero-latency message delivery
2. **Typed, not raw** -- structured messages with types and urgency
3. **Zero external dependencies** -- no Redis, no Docker. Just Go + MCP SDK
4. **Auto-everything** -- broker auto-launches in-process, dead peers auto-clean
5. **One file does one thing** -- clear separation, easy to understand and modify

---

## Architecture

```
  +----------------------------------------------------+
  |              IN-PROCESS BROKER                      |
  |              localhost:7900                          |
  |                                                     |
  |  +------------+  +----------+  +--------------+     |
  |  | In-Memory  |  | HTTP API |  | SSE Streams  |     |
  |  | Store      |  | (REST)   |  | (push to     |     |
  |  |            |  |          |  |  each peer)  |     |
  |  | - peers    |  | register |  |              |     |
  |  | - messages |  | send     |  | /stream/:id  |     |
  |  | - tasks    |  | broadcast|  |              |     |
  |  | - context  |  | task/*   |  | Keeps conn   |     |
  |  |            |  | context  |  | open, pushes |     |
  |  |            |  | etc.     |  | events       |     |
  |  +------------+  +----------+  +--------------+     |
  +------+------------------------------------+---------+
         |                                    |
    HTTP POST                           SSE (persistent)
    (commands)                          (receive messages)
         |                                    |
  +------+------+                      +------+------+
  | MCP Server  |                      | MCP Server  |
  | (stdio)     |                      | (stdio)     |
  |             |                      |             |
  | Claude A    |                      | Claude B    |
  +-------------+                      +-------------+
```

The first MCP server instance to start binds port 7900 and runs the broker in-process. All subsequent instances connect to it as HTTP/SSE clients. There is no separate broker process.

### SSE vs Polling

```
Polling approach (e.g. claude-peers-mcp):
  Server -> POST /poll-messages -> Broker -> response (every 1 second)
  Server -> POST /poll-messages -> Broker -> response (every 1 second)
  Server -> POST /poll-messages -> Broker -> response (FOUND MESSAGE!)
  Latency: 0-999ms. Wasted requests: hundreds per minute.

agentswarm:
  Server <- SSE connection (persistent, open) <- Broker
  (message arrives at broker)
  Broker -- push via SSE --> Server (INSTANT)
  Latency: <10ms. Wasted requests: zero.
```

---

## File Structure

```
agentswarm/
+-- cmd/
|   +-- agentswarm-server/
|       +-- main.go              # Single binary entry point (MCP server + in-process broker)
+-- internal/
|   +-- broker/
|   |   +-- broker.go            # HTTP routes + request handlers
|   |   +-- store.go             # In-memory store (peers, messages, tasks, context)
|   |   +-- sse.go               # SSE connection manager + push logic
|   +-- server/
|   |   +-- mcp.go               # MCP tool definitions + handlers
|   |   +-- stream.go            # SSE client (connects to broker, receives events)
|   |   +-- context.go           # Git root/branch, CWD, TTY, active files detection
|   |   +-- spawn.go             # Agent spawning + prompt building
|   +-- types/
|       +-- types.go             # All shared types (Peer, Message, Task, ContextEntry, etc.)
+-- go.mod
+-- go.sum
+-- Makefile                     # build, install, test, clean
+-- CLAUDE.md                    # Instructions for Claude Code working on this repo
+-- SPEC.md                      # This file
+-- install.sh                   # Shell installer (downloads from GitHub Releases)
+-- .mcp.json                    # MCP server configuration for Claude Code
+-- .github/
|   +-- workflows/
|       +-- ci.yml               # CI: build + test on push/PR
|       +-- release.yml          # Release: cross-compile on tag push
```

### Binary

| Binary | Built from | What it does |
|--------|-----------|-------------|
| `agentswarm-server` | `cmd/agentswarm-server/main.go` | MCP server (stdio) + in-process broker (first instance only) |

### Build

```bash
make build
# Produces: bin/agentswarm-server

make install
# Copies to /usr/local/bin/
```

---

## Data Types

### Peer

```go
type Peer struct {
    ID           string   `json:"id"`            // 8-char random hex
    Name         string   `json:"name"`           // Display name (optional)
    PID          int      `json:"pid"`            // OS process ID
    CWD          string   `json:"cwd"`            // Working directory
    GitRoot      string   `json:"git_root"`       // Git repo root
    GitBranch    string   `json:"git_branch"`     // Current branch
    TTY          string   `json:"tty"`            // Terminal
    Summary      string   `json:"summary"`        // What this peer is working on
    ActiveFiles  []string `json:"active_files"`   // Files being edited (from git diff)
    RegisteredAt string   `json:"registered_at"`  // ISO timestamp
    LastSeen     string   `json:"last_seen"`      // ISO timestamp
}
```

### Message

```go
type MessageType string

const (
    TypeMessage      MessageType = "message"
    TypeQuestion     MessageType = "question"
    TypeResponse     MessageType = "response"
    TypeAlert        MessageType = "alert"
    TypeNotification MessageType = "notification"
    TypeRequest      MessageType = "request"
    TypeBroadcast    MessageType = "broadcast"
)

type Message struct {
    ID        int64           `json:"id"`          // Auto-increment
    Type      MessageType     `json:"type"`
    FromID    string          `json:"from_id"`     // Sender peer ID
    ToID      string          `json:"to_id"`       // Empty for broadcasts
    Text      string          `json:"text"`        // Message content
    Context   json.RawMessage `json:"context"`     // Optional structured context
    Scope     string          `json:"scope"`       // For broadcasts: machine/directory/repo
    SentAt    string          `json:"sent_at"`     // ISO timestamp
    Delivered bool            `json:"delivered"`
}
```

### Task

```go
type Task struct {
    TaskID      string `json:"task_id"`       // Unique task ID
    ParentID    string `json:"parent_id"`     // Peer that created the task
    ChildID     string `json:"child_id"`      // Peer assigned to the task
    Prompt      string `json:"prompt"`        // Task description
    Status      string `json:"status"`        // pending, completed, failed, cancelled
    Result      string `json:"result"`        // Result text (set on completion)
    CreatedAt   string `json:"created_at"`    // ISO timestamp
    CompletedAt string `json:"completed_at"`  // ISO timestamp
}
```

### ContextEntry

```go
type ContextEntry struct {
    Key        string `json:"key"`
    ScopeType  string `json:"scope_type"`   // machine, directory, repo
    ScopeValue string `json:"scope_value"`  // Actual path or "machine"
    Value      string `json:"value"`
    SetBy      string `json:"set_by"`       // Peer ID
    UpdatedAt  string `json:"updated_at"`   // ISO timestamp
}
```

---

## Broker API

All endpoints use JSON request/response bodies. The broker runs on `localhost:7900`.

### Peer Management

| Method | Endpoint | Request Body | Response | Description |
|--------|----------|-------------|----------|-------------|
| `POST` | `/register` | `{ pid, name?, cwd, git_root?, git_branch?, tty?, summary?, active_files? }` | `{ id }` | Register new peer, receive assigned ID |
| `POST` | `/unregister` | `{ id }` | `{ ok }` | Remove peer |
| `POST` | `/heartbeat` | `{ id, active_files?, git_branch? }` | `{ ok }` | Keep-alive + update context |
| `POST` | `/set-summary` | `{ id, summary }` | `{ ok }` | Update peer summary |
| `POST` | `/set-name` | `{ id, name }` | `{ ok }` | Update peer display name |
| `POST` | `/list-peers` | `{ scope, cwd?, git_root?, exclude_id? }` | `Peer[]` | Discover peers (scope: machine, directory, repo) |

### Messaging

| Method | Endpoint | Request Body | Response | Description |
|--------|----------|-------------|----------|-------------|
| `POST` | `/send` | `{ from_id, to_id, type?, text, context? }` | `{ ok, message_id }` | Send message to one peer |
| `POST` | `/broadcast` | `{ from_id, scope, type?, text, context?, cwd?, git_root? }` | `{ ok, sent_to }` | Send to all peers in scope |

### Shared Context

| Method | Endpoint | Request Body | Response | Description |
|--------|----------|-------------|----------|-------------|
| `POST` | `/context/set` | `{ peer_id, key, value, scope?, scope_value? }` | `{ ok }` | Set shared context value |
| `POST` | `/context/get` | `{ key, scope?, scope_value? }` | `{ value, set_by, updated_at }` | Get shared context value |
| `POST` | `/context/list` | `{ scope?, scope_value? }` | `{ entries: ContextEntry[] }` | List all context keys |

### Task Orchestration

| Method | Endpoint | Request Body | Response | Description |
|--------|----------|-------------|----------|-------------|
| `POST` | `/task/create` | `{ parent_id, child_id?, prompt }` | `{ task_id }` | Create a new task |
| `POST` | `/task/update` | `{ task_id, child_id?, status, result? }` | `{ ok }` | Update task status/result |
| `POST` | `/task/wait` | `{ task_ids, mode?, timeout_seconds? }` | `{ results, timed_out }` | Block until tasks complete (mode: "any" or "all") |
| `POST` | `/task/list` | `{ parent_id?, task_ids? }` | `{ tasks }` | List tasks by parent or IDs |
| `POST` | `/task/cancel` | `{ task_id }` | `{ ok }` | Cancel a task |

### SSE Stream

```
GET /stream/{peer_id}
```

Persistent Server-Sent Events connection. The broker pushes events as they happen:

```
event: message
data: {"id":1,"type":"question","from_id":"abc12345","text":"what files are you editing?","sent_at":"2026-04-01T12:00:00Z"}

event: broadcast
data: {"id":2,"type":"alert","from_id":"def67890","text":"refactored auth -- heads up","scope":"repo","sent_at":"2026-04-01T12:00:05Z"}

event: context_updated
data: {"key":"api_schema","set_by":"abc12345","updated_at":"2026-04-01T12:00:10Z"}

event: peer_joined
data: {"id":"ghi11111","cwd":"/Users/scott/myproject","summary":"Working on tests"}

event: peer_left
data: {"id":"xyz00000","reason":"process_exited"}
```

On initial connection, any undelivered messages for the peer are flushed immediately. A keepalive comment (`: keepalive`) is sent every 15 seconds.

### Health Check

```
GET /health -> { status: "ok", service: "agentswarm", peers: 5, uptime_seconds: 3600 }
```

---

## MCP Tools

These are the tools exposed to Claude Code via the MCP stdio protocol.

### list_peers

Discover other Claude Code instances. Shows what they are working on and what files they are editing.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `scope` | string (enum: machine, directory, repo) | yes | Discovery scope |

### send_message

Send a message to another Claude Code instance. Arrives instantly via SSE.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `to_id` | string | yes | Target peer ID |
| `text` | string | yes | Message content |
| `type` | string (enum: message, question, request, alert, notification) | no | Message type (default: message) |
| `files` | string[] | no | Relevant file paths for context |

### broadcast

Send a message to all peers in a scope.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | yes | Message content |
| `scope` | string (enum: machine, directory, repo) | yes | Broadcast scope |
| `type` | string (enum: message, alert, notification) | no | Message type |

### set_summary

Describe what you are working on (visible to other peers).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `summary` | string | yes | 1-2 sentence summary |

### set_name

Set a human-readable name for this peer.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | yes | Display name |

### whoami

Returns your own peer ID, name, and registration info. No parameters.

### get_context

Read a shared context value set by any peer in the same scope.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `key` | string | yes | Context key to read |

### set_context

Set a shared context value visible to all peers in the same repo/directory.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `key` | string | yes | Context key |
| `value` | string | yes | Context value (can be JSON stringified) |

### check_messages

Backward-compatible alias for `wait_for_messages` with timeout 0. Returns immediately with any pending messages. No parameters.

### wait_for_messages

Block until messages arrive for this peer. Useful for interactive agents that need to stay alive waiting for work.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `timeout_seconds` | number | no | How long to wait (default: 120s, 0 = non-blocking check) |

### spawn_agent

Launch a new Claude Code agent that joins the swarm. Returns immediately with the process PID.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `prompt` | string | yes | Task/instruction for the new agent |
| `cwd` | string | no | Working directory (defaults to current peer's cwd) |
| `name` | string | no | Display name for the spawned agent |
| `mode` | string (enum: fire-and-forget, interactive) | no | fire-and-forget (default): agent runs task and exits. interactive: agent stays alive for multi-turn messaging. |

The spawned agent runs as a detached `claude` subprocess with `--dangerously-skip-permissions --dangerously-load-development-channels`. Its prompt is augmented with swarm context (parent peer ID, mode instructions). In fire-and-forget mode, the agent is told to send results back to the parent via send_message. In interactive mode, it stays alive and responds to incoming channel messages.

---

## Orchestration

Orchestration lets a parent agent delegate work to child agents and wait for results. This is built on top of spawn_agent and the task broker endpoints.

### Workflow: delegate

1. Parent calls `spawn_agent` to create a child agent with a prompt.
2. Parent calls `/task/create` to register a task, associating it with the child.
3. Child does its work, then calls `/task/update` to report completion (status: completed, result: ...).
4. Parent calls `/task/wait` to block until the task finishes, then reads the result.

### MCP Tools for Orchestration

These tools wrap the task broker endpoints for use by Claude Code agents:

| Tool | Description |
|------|-------------|
| `delegate` | Spawn a child agent and create a task for it. Combines spawn_agent + task/create. Returns the task ID. |
| `request_task` | Create a task targeting an already-running peer (no spawn). |
| `report_result` | Called by the child to report task completion or failure. |
| `wait_for_result` | Block until one or more tasks complete. Supports "any" and "all" modes with optional timeout. |
| `list_tasks` | List tasks by parent ID or specific task IDs. |
| `cancel_task` | Cancel a pending task. |
| `wait_for_messages` | Block until a message arrives (useful for interactive agents waiting for work). |

### Task Lifecycle

```
  Parent                        Broker                       Child
    |                             |                            |
    |-- delegate (prompt) ------->|                            |
    |<-- task_id -----------------| -- spawn_agent ----------->|
    |                             |<--- register --------------|
    |                             |<--- /task/update (child) --|
    |                             |                            |
    |                             |     (child does work)      |
    |                             |                            |
    |                             |<--- /task/update --------- |
    |                             |     status: completed      |
    |-- wait_for_result --------->|     result: "..."          |
    |<-- results[] ---------------|                            |
```

---

## SSE Implementation

### Broker Side (sse.go)

The SSEManager maintains a map of peer ID to buffered channel. Each SSE connection runs in its own goroutine.

```go
type SSEManager struct {
    mu    sync.RWMutex
    conns map[string]chan SSEEvent
}
```

- `Subscribe(peerID)` -- creates a buffered channel (capacity 64) and registers it.
- `Unsubscribe(peerID)` -- closes and removes the channel.
- `Push(peerID, event)` -- non-blocking send to a specific peer. Returns false if peer is not connected or channel is full.
- `Broadcast(event, exclude)` -- sends to all connected peers except the excluded one. Skips slow peers.

### Client Side (stream.go)

The SSEClient connects to `GET /stream/{peer_id}` and parses the event stream. On disconnect, it reconnects after a 2-second backoff. Events are dispatched to a callback that converts them into MCP channel notifications (`notifications/claude/channel`).

---

## In-Memory Store

The store (`internal/broker/store.go`) uses a `sync.RWMutex` to protect all data structures:

- `peers map[string]*Peer` -- registered peers keyed by ID
- `messages []Message` -- append-only message log with delivery tracking
- `context map[string]*ContextEntry` -- shared context keyed by composite key (scopeType + scopeValue + key)
- `tasks` -- task records for orchestration (keyed by task ID)

Dead peer cleanup runs every 30 seconds. A peer is considered stale if its `last_seen` timestamp exceeds the timeout (60 seconds) and its OS process is no longer alive.

---

## MCP Server Startup Sequence

```
1. Start MCP stdio server (immediately ready for tool calls)
2. In background:
   a. Check if broker is running on :7900 (GET /health)
   b. If not, bind port 7900 and start broker in-process
   c. If port is taken, wait for the other instance's broker to become healthy
3. Detect CWD, git root, git branch, TTY, active files
4. Register with broker (POST /register) -> receive peer ID
5. Open SSE connection to GET /stream/{peer_id}
6. Start heartbeat timer (every 15s -- update active_files, branch)
7. SSE events -> convert to notifications/claude/channel for Claude Code
8. On exit: unregister, close SSE, shut down in-process broker if running
```

---

## MCP Instructions (System Prompt)

The MCP server sends these instructions to Claude Code:

```
You are connected to agentswarm. Other Claude Code instances can see you and message you.

RULES:
1. When you receive a <channel source="agentswarm"> message, RESPOND IMMEDIATELY.
   Pause your current work, reply using send_message, then resume.
2. On your first turn, call set_summary to describe what you're working on.
3. Before editing a shared file, call list_peers to check if anyone else is working on it.
4. Use get_context/set_context to share API contracts, decisions, or schemas with peers.
5. Use broadcast for announcements that affect everyone (refactors, breaking changes).
6. Use appropriate message types:
   - "question" when you need a response
   - "alert" for urgent conflicts or breaking changes
   - "notification" for FYI status updates
   - "request" when delegating a task to another peer
7. Use delegate to spawn tracked agents. Use wait_for_result to collect their output.
8. When you receive a request with a task_id, call report_result with that task_id when you're done.
9. Use cancel_task after wait_for_result(mode: "any") to clean up remaining agents.
```

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTSWARM_PORT` | `7900` | Broker port |
| `AGENTSWARM_HEARTBEAT_MS` | `15000` | Heartbeat interval |

---

## Dependencies

```go
module github.com/scott-pallas/agentswarm

go 1.24

require github.com/mark3labs/mcp-go v0.26.0
```

One dependency. No CGo. Cross-compiles cleanly.

---

## MCP Configuration (.mcp.json)

```json
{
  "mcpServers": {
    "agentswarm": {
      "type": "stdio",
      "command": "agentswarm-server"
    }
  }
}
```

Or add globally for all Claude Code sessions:
```bash
claude mcp add --scope user --transport stdio agentswarm -- agentswarm-server
```

---

## Requirements

- Go 1.24 (for building)
- Nothing at runtime (single static binary)
- Claude Code with channels support
- claude.ai login (channels require it -- API key auth does not work)
