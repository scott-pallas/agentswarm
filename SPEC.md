# agentswarm — Technical Specification

> Real-time peer-to-peer communication for Claude Code sessions.
> Instant delivery, structured messaging, zero polling.

**Author:** Scott Pallas
**Version:** 0.1.0
**Date:** 2026-03-30
**Language:** Go
**Runtime:** Single binary (no runtime dependencies)
**Protocol:** MCP (Model Context Protocol)
**Repo:** github.com/scott-pallas/agentswarm

---

## Overview

agentswarm enables multiple Claude Code sessions to discover each other and communicate in real-time on the same machine. It improves on existing solutions (claude-peers-mcp) by replacing HTTP polling with Server-Sent Events (SSE) for instant message delivery, adding structured message types, conversation threading, broadcast messaging, file conflict detection, and a shared key-value context store.

### Design Principles

1. **Instant, not polled** — SSE push for zero-latency message delivery
2. **Typed, not raw** — structured messages with types, threading, and urgency
3. **Zero external dependencies** — no OpenAI, no Redis, no Docker. Just Bun + SQLite + MCP SDK
4. **Auto-everything** — broker auto-launches, dead peers auto-clean, summaries auto-set
5. **One file does one thing** — clear separation, easy to understand and modify

---

## Architecture

```
  ┌────────────────────────────────────────────────┐
  │              BROKER (broker.ts)                  │
  │              localhost:7900                      │
  │                                                  │
  │  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │
  │  │ SQLite   │  │ HTTP API │  │ SSE Streams  │  │
  │  │          │  │ (REST)   │  │ (push to     │  │
  │  │ • peers  │  │          │  │  each peer)  │  │
  │  │ • msgs   │  │ register │  │              │  │
  │  │ • context│  │ send     │  │ /stream/:id  │  │
  │  │ • threads│  │ broadcast│  │              │  │
  │  │          │  │ context  │  │ Keeps conn   │  │
  │  │          │  │ etc.     │  │ open, pushes │  │
  │  │          │  │          │  │ events       │  │
  │  └──────────┘  └──────────┘  └──────────────┘  │
  └──────┬─────────────────────────────┬────────────┘
         │                             │
    HTTP POST                    SSE (persistent)
    (commands)                   (receive messages)
         │                             │
  ┌──────┴──────┐               ┌──────┴──────┐
  │ MCP Server  │               │ MCP Server  │
  │ (server.ts) │               │ (server.ts) │
  │             │               │             │
  │ stdio ↕     │               │ stdio ↕     │
  │ Claude A    │               │ Claude B    │
  └─────────────┘               └─────────────┘
```

### Key Difference from claude-peers

```
claude-peers:
  Server → POST /poll-messages → Broker → response (every 1 second)
  Server → POST /poll-messages → Broker → response (every 1 second)
  Server → POST /poll-messages → Broker → response (FOUND MESSAGE!)
  Latency: 0-999ms. Wasted requests: hundreds per minute.

agentswarm:
  Server ← SSE connection (persistent, open) ← Broker
  (message arrives at broker)
  Broker ── push via SSE ──► Server (INSTANT)
  Latency: <10ms. Wasted requests: zero.
```

---

## File Structure

```
agentswarm/
├── cmd/
│   ├── broker/
│   │   └── main.go          # Broker binary entry point
│   └── server/
│       └── main.go          # MCP server binary entry point
├── internal/
│   ├── broker/
│   │   ├── broker.go        # HTTP routes + request handlers
│   │   ├── db.go            # SQLite schema, queries, prepared statements
│   │   └── sse.go           # SSE connection manager + push logic
│   ├── server/
│   │   ├── mcp.go           # MCP tool definitions + handlers
│   │   ├── stream.go        # SSE client (connects to broker, receives events)
│   │   └── context.go       # Git root/branch, CWD, TTY, active files detection
│   └── types/
│       └── types.go         # All shared types (Peer, Message, Thread, etc.)
├── cli/
│   └── main.go              # CLI utility (status, peers, send, broadcast, etc.)
├── go.mod
├── go.sum
├── Makefile                  # build, install, test, clean
├── CLAUDE.md                 # Instructions for Claude Code working on this repo
├── SPEC.md                   # This file
├── README.md                 # User-facing docs
└── .mcp.json                 # MCP server configuration
```

### Binaries

| Binary | Built from | What it does |
|--------|-----------|-------------|
| `agentswarm-broker` | `cmd/broker/main.go` | Singleton daemon — HTTP + SSE + SQLite |
| `agentswarm-server` | `cmd/server/main.go` | MCP server — one per Claude Code session |
| `agentswarm` | `cli/main.go` | CLI utility for debugging + management |

### Build

```bash
make build
# Produces:
#   bin/agentswarm-broker
#   bin/agentswarm-server
#   bin/agentswarm

make install
# Copies to /usr/local/bin/
```

---

## Message Protocol

### Message Types

```typescript
type MessageType = 
  | "message"      // General communication
  | "question"     // Expects a response
  | "response"     // Reply to a question
  | "alert"        // Urgent — file conflict, error, etc.
  | "notification" // FYI — status update, completion, etc.
  | "request"      // Task delegation ("run these tests for me")
  | "broadcast"    // Sent to multiple peers
```

### Message Schema

```typescript
interface PeerMessage {
  id: number;                    // Auto-increment
  type: MessageType;
  from_id: string;               // Sender peer ID
  to_id: string | null;          // Null for broadcasts
  thread_id: string | null;      // Groups related messages
  reply_to: number | null;       // References a specific message ID
  text: string;                  // Message content
  urgency: "low" | "normal" | "high";
  context?: {
    files?: string[];            // Relevant file paths
    diff?: string;               // Code changes (optional)
    metadata?: Record<string, unknown>; // Extensible
  };
  sent_at: string;               // ISO timestamp
  delivered: boolean;
}
```

### Peer Schema

```typescript
interface Peer {
  id: string;                    // 8-char random ID
  pid: number;                   // OS process ID
  cwd: string;                   // Working directory
  git_root: string | null;       // Git repo root
  git_branch: string | null;     // Current branch
  tty: string | null;            // Terminal
  summary: string;               // What this peer is working on
  active_files: string[];        // Files currently being edited (from git diff)
  registered_at: string;
  last_seen: string;
}
```

### Thread Schema

```typescript
interface Thread {
  id: string;                    // UUID or short ID
  topic: string;                 // Brief description
  participants: string[];        // Peer IDs involved
  created_at: string;
  last_activity: string;
}
```

---

## Broker API

### Core Endpoints (HTTP POST)

| Endpoint | Request Body | Response | Description |
|----------|-------------|----------|-------------|
| `POST /register` | `{ pid, cwd, git_root, git_branch, tty, summary, active_files }` | `{ id }` | Register new peer, get ID |
| `POST /unregister` | `{ id }` | `{ ok }` | Remove peer |
| `POST /heartbeat` | `{ id, active_files?, git_branch? }` | `{ ok }` | Keep-alive + update context |
| `POST /set-summary` | `{ id, summary }` | `{ ok }` | Update peer summary |
| `POST /list-peers` | `{ scope, cwd, git_root, exclude_id? }` | `Peer[]` | Discover peers |
| `POST /send` | `{ from_id, to_id, type, text, thread_id?, reply_to?, urgency?, context? }` | `{ ok, message_id }` | Send message to one peer |
| `POST /broadcast` | `{ from_id, scope, type, text, urgency?, context? }` | `{ ok, sent_to: string[] }` | Send to all peers in scope |
| `POST /context/set` | `{ peer_id, key, value, scope? }` | `{ ok }` | Set shared context |
| `POST /context/get` | `{ key, scope? }` | `{ value, set_by, updated_at }` | Get shared context |
| `POST /context/list` | `{ scope? }` | `{ entries: ContextEntry[] }` | List all context keys |

### SSE Stream Endpoint

```
GET /stream/:peer_id
```

Persistent Server-Sent Events connection. Broker pushes events as they happen:

```
event: message
data: {"id":42,"type":"question","from_id":"abc123","text":"what files are you editing?","urgency":"normal","sent_at":"2026-03-30T21:00:00Z"}

event: broadcast
data: {"id":43,"type":"alert","from_id":"def456","text":"I just refactored auth.ts - heads up","scope":"repo","sent_at":"2026-03-30T21:00:05Z"}

event: conflict
data: {"file":"src/auth.ts","peers":["abc123","def456"],"detected_at":"2026-03-30T21:00:10Z"}

event: context_updated
data: {"key":"api_schema","set_by":"abc123","updated_at":"2026-03-30T21:00:15Z"}

event: peer_joined
data: {"id":"ghi789","cwd":"/Users/scott/myproject","summary":"Working on tests"}

event: peer_left
data: {"id":"xyz000","reason":"process_exited"}
```

### Health Check

```
GET /health → { status: "ok", peers: 5, uptime_seconds: 3600 }
```

---

## MCP Tools

### Tool: `list_peers`

```typescript
{
  name: "list_peers",
  description: "Discover other Claude Code instances. Shows what they're working on and what files they're editing.",
  inputSchema: {
    type: "object",
    properties: {
      scope: {
        type: "string",
        enum: ["machine", "directory", "repo"],
        description: "Discovery scope"
      }
    },
    required: ["scope"]
  }
}
```

### Tool: `send_message`

```typescript
{
  name: "send_message",
  description: "Send a message to another Claude Code instance. Arrives instantly.",
  inputSchema: {
    type: "object",
    properties: {
      to_id: { type: "string", description: "Target peer ID" },
      text: { type: "string", description: "Message content" },
      type: { 
        type: "string", 
        enum: ["message", "question", "request", "alert", "notification"],
        description: "Message type. Use 'question' when expecting a reply, 'alert' for urgent items, 'request' for task delegation."
      },
      urgency: {
        type: "string",
        enum: ["low", "normal", "high"],
        description: "Message urgency. 'high' interrupts immediately."
      },
      thread_id: { type: "string", description: "Thread ID for ongoing conversations (optional)" },
      files: { 
        type: "array", 
        items: { type: "string" },
        description: "Relevant file paths for context (optional)" 
      }
    },
    required: ["to_id", "text"]
  }
}
```

### Tool: `broadcast`

```typescript
{
  name: "broadcast",
  description: "Send a message to all peers in a scope (repo, directory, or machine).",
  inputSchema: {
    type: "object",
    properties: {
      text: { type: "string" },
      scope: { type: "string", enum: ["machine", "directory", "repo"] },
      type: { type: "string", enum: ["message", "alert", "notification"] },
      urgency: { type: "string", enum: ["low", "normal", "high"] }
    },
    required: ["text", "scope"]
  }
}
```

### Tool: `set_summary`

```typescript
{
  name: "set_summary",
  description: "Describe what you're working on (visible to other peers).",
  inputSchema: {
    type: "object",
    properties: {
      summary: { type: "string", description: "1-2 sentence summary" }
    },
    required: ["summary"]
  }
}
```

### Tool: `get_context`

```typescript
{
  name: "get_context",
  description: "Read a shared context value set by any peer in the same scope. Use for API contracts, architectural decisions, shared state.",
  inputSchema: {
    type: "object",
    properties: {
      key: { type: "string", description: "Context key to read" }
    },
    required: ["key"]
  }
}
```

### Tool: `set_context`

```typescript
{
  name: "set_context",
  description: "Set a shared context value visible to all peers in the same repo/directory. Use for API contracts, decisions, shared state.",
  inputSchema: {
    type: "object",
    properties: {
      key: { type: "string", description: "Context key" },
      value: { type: "string", description: "Context value (can be JSON stringified)" }
    },
    required: ["key", "value"]
  }
}
```

### Tool: `check_messages`

```typescript
{
  name: "check_messages",
  description: "Manually check for messages. Normally messages arrive automatically via push — use this as a fallback.",
  inputSchema: {
    type: "object",
    properties: {}
  }
}
```

---

## SQLite Schema

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 3000;

CREATE TABLE IF NOT EXISTS peers (
  id TEXT PRIMARY KEY,
  pid INTEGER NOT NULL,
  cwd TEXT NOT NULL,
  git_root TEXT,
  git_branch TEXT,
  tty TEXT,
  summary TEXT NOT NULL DEFAULT '',
  active_files TEXT NOT NULL DEFAULT '[]',  -- JSON array
  registered_at TEXT NOT NULL,
  last_seen TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL DEFAULT 'message',
  from_id TEXT NOT NULL,
  to_id TEXT,                                -- NULL for broadcasts
  thread_id TEXT,
  reply_to INTEGER,
  text TEXT NOT NULL,
  urgency TEXT NOT NULL DEFAULT 'normal',
  context TEXT,                              -- JSON blob
  scope TEXT,                                -- For broadcasts: machine/directory/repo
  sent_at TEXT NOT NULL,
  delivered INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (from_id) REFERENCES peers(id),
  FOREIGN KEY (reply_to) REFERENCES messages(id)
);

CREATE TABLE IF NOT EXISTS context (
  key TEXT NOT NULL,
  scope_type TEXT NOT NULL DEFAULT 'repo',   -- machine/directory/repo
  scope_value TEXT NOT NULL,                 -- the actual path/root
  value TEXT NOT NULL,
  set_by TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (key, scope_type, scope_value),
  FOREIGN KEY (set_by) REFERENCES peers(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_to_id ON messages(to_id, delivered);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_scope ON messages(scope, delivered);
CREATE INDEX IF NOT EXISTS idx_peers_git_root ON peers(git_root);
CREATE INDEX IF NOT EXISTS idx_peers_cwd ON peers(cwd);
```

---

## SSE Implementation (Broker Side — Go)

```go
// internal/broker/sse.go — the key architectural change

// SSEManager manages persistent SSE connections for all peers
type SSEManager struct {
    mu    sync.RWMutex
    conns map[string]chan SSEEvent  // peerId → event channel
}

type SSEEvent struct {
    Event string      // "message", "broadcast", "conflict", etc.
    Data  interface{} // JSON-serializable payload
}

func NewSSEManager() *SSEManager {
    return &SSEManager{conns: make(map[string]chan SSEEvent)}
}

// Subscribe — called when a peer connects to GET /stream/:id
func (m *SSEManager) Subscribe(peerId string) chan SSEEvent {
    m.mu.Lock()
    defer m.mu.Unlock()
    ch := make(chan SSEEvent, 64) // buffered to avoid blocking
    m.conns[peerId] = ch
    return ch
}

// Unsubscribe — called when SSE connection drops
func (m *SSEManager) Unsubscribe(peerId string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    if ch, ok := m.conns[peerId]; ok {
        close(ch)
        delete(m.conns, peerId)
    }
}

// Push — send an event to a specific peer (non-blocking)
func (m *SSEManager) Push(peerId string, event SSEEvent) bool {
    m.mu.RLock()
    defer m.mu.RUnlock()
    ch, ok := m.conns[peerId]
    if !ok {
        return false
    }
    select {
    case ch <- event:
        return true
    default:
        return false // channel full, peer is slow
    }
}

// Broadcast — send to all peers matching a filter
func (m *SSEManager) Broadcast(event SSEEvent, exclude string) []string {
    m.mu.RLock()
    defer m.mu.RUnlock()
    var sent []string
    for id, ch := range m.conns {
        if id == exclude {
            continue
        }
        select {
        case ch <- event:
            sent = append(sent, id)
        default:
            // skip slow peers
        }
    }
    return sent
}

// HTTP handler for GET /stream/:id
// Each connection gets its own goroutine
func (b *Broker) handleSSEStream(w http.ResponseWriter, r *http.Request) {
    peerId := r.PathValue("id") // Go 1.22+ path params
    
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "SSE not supported", 500)
        return
    }
    
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    
    ch := b.sse.Subscribe(peerId)
    defer b.sse.Unsubscribe(peerId)
    
    // Send initial connection event
    fmt.Fprintf(w, ": connected\n\n")
    flusher.Flush()
    
    // Keepalive ticker
    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case event, ok := <-ch:
            if !ok {
                return // channel closed
            }
            data, _ := json.Marshal(event.Data)
            fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, data)
            flusher.Flush()
            
        case <-ticker.C:
            fmt.Fprintf(w, ": keepalive\n\n")
            flusher.Flush()
            
        case <-r.Context().Done():
            return // client disconnected
        }
    }
}
```

**Why Go is perfect for this:** Each SSE connection runs in its own goroutine. 100 peers = 100 goroutines = ~200KB of memory. In Node/Bun, you'd be fighting the event loop. Go handles this natively.

---

## File Conflict Detection

The broker can detect when two peers are editing the same file:

```typescript
// On heartbeat, peers report their active_files (from git diff --name-only)
// Broker checks for overlaps:

function detectConflicts(peerId: string, activeFiles: string[]) {
  const peers = selectAllPeers.all() as Peer[];
  
  for (const other of peers) {
    if (other.id === peerId) continue;
    
    const otherFiles = JSON.parse(other.active_files) as string[];
    const overlap = activeFiles.filter(f => otherFiles.includes(f));
    
    if (overlap.length > 0) {
      // Push conflict alert to BOTH peers
      const alert = {
        file: overlap,
        peers: [peerId, other.id],
        detected_at: new Date().toISOString(),
      };
      pushToPeer(peerId, "conflict", alert);
      pushToPeer(other.id, "conflict", alert);
    }
  }
}
```

---

## MCP Server Startup Sequence

```
1. Ensure broker is running (auto-launch if not)
2. Detect CWD, git root, git branch, TTY
3. Get active files (git diff --name-only)
4. Register with broker → receive peer ID
5. Open SSE connection to /stream/:peer_id
6. Connect MCP over stdio
7. Listen for SSE events → push as claude/channel notifications
8. Start heartbeat timer (every 15s — update active_files, branch)
9. Set instructions telling Claude to call set_summary on first turn
10. On exit: unregister, close SSE connection
```

---

## MCP Instructions (System Prompt for Claude)

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
```

---

## CLI Commands

```bash
bun cli.ts status              # Broker status + all peers
bun cli.ts peers               # List all registered peers
bun cli.ts send <id> <msg>     # Send message to a peer
bun cli.ts broadcast <msg>     # Broadcast to all peers
bun cli.ts context list        # Show all shared context
bun cli.ts context get <key>   # Get a context value
bun cli.ts context set <k> <v> # Set a context value
bun cli.ts threads             # List active threads
bun cli.ts kill-broker         # Stop broker daemon
```

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTSWARM_PORT` | `7900` | Broker port |
| `AGENTSWARM_DB` | `~/.agentswarm.db` | SQLite database path |
| `AGENTSWARM_HEARTBEAT_MS` | `15000` | Heartbeat interval |
| `AGENTSWARM_KEEPALIVE_MS` | `15000` | SSE keepalive interval |
| `AGENTSWARM_STALE_TIMEOUT_MS` | `60000` | Remove peers not seen for this long |

---

## Dependencies (go.mod)

```go
module github.com/scott-pallas/agentswarm

go 1.24

require (
    github.com/mark3labs/mcp-go v0.26.0    // MCP SDK for Go
    modernc.org/sqlite v1.37.0              // Pure Go SQLite (no CGo)
)
```

**Two dependencies. No CGo. Cross-compiles cleanly.**

If the Go MCP SDK has any gaps, fallback plan: implement MCP stdio JSON-RPC manually (~100 lines). The protocol is simple:
- Read JSON-RPC from stdin
- Write JSON-RPC to stdout  
- Handle `tools/list` and `tools/call` methods
- Send `notifications/claude/channel` for push messages

---

## Requirements

- Go 1.22+ (for building)
- Nothing at runtime (single static binary)
- Claude Code v2.1.80+ (channels support)
- claude.ai login (channels require it — API key auth won't work)

---

## Build Order (for implementation)

1. `internal/types/types.go` — all shared types
2. `internal/broker/db.go` — SQLite schema + prepared statements
3. `internal/broker/sse.go` — SSE connection manager
4. `internal/broker/broker.go` — HTTP routes + handlers + wire it all together
5. `cmd/broker/main.go` — broker binary entry point
6. `internal/server/context.go` — git/CWD/TTY/active files detection
7. `internal/server/stream.go` — SSE client (connects to broker)
8. `internal/server/mcp.go` — MCP tool definitions + handlers
9. `cmd/server/main.go` — MCP server binary entry point
10. `cli/main.go` — debugging utility
11. `Makefile` — build, install, test, clean
12. `CLAUDE.md` — instructions for Claude Code working on this repo
13. `README.md` — user docs
14. `.mcp.json` — MCP configuration
15. Test: open 2 Claude Code sessions, have them talk

---

## Success Criteria

- [ ] Two Claude Code sessions can discover each other via `list_peers`
- [ ] Messages arrive instantly via SSE (not polling)
- [ ] Broadcasts reach all peers in a scope
- [ ] Threads maintain conversation context
- [ ] File conflict detection warns both peers
- [ ] Shared context (get/set) works across peers
- [ ] Broker auto-launches and auto-cleans dead peers
- [ ] Clean shutdown on SIGINT/SIGTERM
- [ ] Total codebase under 1000 lines
- [ ] Single dependency (@modelcontextprotocol/sdk)
- [ ] Works with Claude Code auto mode

---

## Makefile

```makefile
.PHONY: build install test clean

BINDIR := bin

build:
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
```

## MCP Configuration (.mcp.json)

```json
{
  "agentswarm": {
    "command": "agentswarm-server",
    "args": []
  }
}
```

Or add globally for all Claude Code sessions:
```bash
claude mcp add --scope user --transport stdio agentswarm -- agentswarm-server
```

---

*Spec written by SpicyIcyBot 🌶️❄️ for FrostByte to implement*
