# Orchestration Layer Design

## Problem

Agentswarm enables multiple Claude Code sessions to discover and message each other, but there's no way to compose agents into workflows. A developer can spawn agents, but can't say "do A, then B with A's output, then C and D in parallel, then synthesize." The missing piece is orchestration — structured task delegation with result tracking.

## Goals

- A single developer can spin up a team of agents that coordinate automatically
- An orchestrator agent (full Claude Code session) decides what to delegate, collects results, and drives the next step
- Both spawned agents and human-started sessions can participate as workers
- No workflow definition language — Claude's reasoning is the orchestration engine
- Reliable: tasks are tracked, failures are detected, results are collected

## Non-Goals

- Declarative workflow graphs (YAML/JSON DAGs)
- Built-in retry policies or circuit breakers (orchestrator handles this)
- Persistent workflow state across orchestrator restarts
- Cross-machine orchestration
- New dependencies

## Design

### New MCP Tools

#### `wait_for_messages`

Blocks until one or more messages arrive for this peer. The existing `check_messages` tool is kept as a backward-compatible alias that calls `wait_for_messages` with `timeout_seconds: 0` (non-blocking). This is the foundation that keeps interactive agents alive.

Under the hood, the MCP server waits on a local channel fed by the SSE event stream. When a message arrives via SSE, it's forwarded to the channel, unblocking the tool call.

```
wait_for_messages(timeout_seconds?: number) → { messages: Message[], timed_out: bool }
```

- `timeout_seconds`: How long to block. Default 120s. 0 = non-blocking check.
- Returns accumulated messages and whether the timeout was hit.
- If no messages arrive before timeout, returns empty array with `timed_out: true`.

#### `delegate`

Spawns a new agent with a tracked task. Combines `spawn_agent` + task creation in one atomic operation. Returns immediately — does not wait for the agent to finish.

```
delegate(
  prompt: string,
  name?: string,
  cwd?: string
) → { task_id: string, pid: number }
```

- Creates a task record in the broker (status: `pending`, child_id empty).
- Spawns a new Claude Code agent with an augmented prompt that includes the `task_id` and instructions to call `report_result` when done.
- Task linking is implicit: when a worker calls `report_result` for a task that has no `child_id`, the broker sets `child_id` from the caller's peer ID. The task transitions directly from `pending` to its terminal status (`completed`/`failed`). There is no separate `running` state for delegated tasks — the agent either reports back or is detected as dead by the cleaner.

#### `request_task`

Assigns a tracked task to an existing peer (e.g., a human-started session). Does not spawn a new process — sends the request as a channel message with the task_id embedded.

```
request_task(
  peer_id: string,
  prompt: string
) → { task_id: string }
```

- Creates a task record in the broker (status: `pending`, child_id: target peer).
- Sends a message to the target peer with type `request`, including the task_id in metadata.
- Target peer sees the request as a channel notification and is expected to call `report_result` when done.
- Task status is set to `running` immediately (the peer is already alive).

#### `report_result`

Called by a worker agent (spawned or human-started) to report task completion. Unblocks any pending `wait_for_result` calls from the parent.

```
report_result(
  task_id: string,
  result: string,
  status?: "completed" | "failed"
) → { ok: bool }
```

- Updates the task record in the broker (status, result, completed_at).
- Also sends the result as a `send_message` to the parent peer for SSE push.
- Default status is `completed`.
- Result string is capped at 64KB. If the worker needs to communicate more, it should write to a file and report the path.

#### `wait_for_result`

Blocks until one or more delegated tasks reach a terminal state (completed, failed, or timeout).

```
wait_for_result(
  task_id: string | string[],
  mode?: "any" | "all",
  timeout_seconds?: number
) → { results: TaskResult[], timed_out: bool }
```

- `mode: "all"` (default): Waits until every task_id is terminal.
- `mode: "any"`: Returns as soon as any one task reaches terminal state.
- `timeout_seconds`: Default 300s. If hit, returns whatever results are available with `timed_out: true`.
- `TaskResult`: `{ task_id, status, result, peer_id, completed_at }`

Under the hood, calls the broker's `/task/wait` long-poll endpoint.

#### `list_tasks`

Non-blocking query to check the status of delegated tasks. Unlike `wait_for_result`, this returns immediately with current state — useful for polling or progress checks.

```
list_tasks(
  task_ids?: string[]
) → { tasks: Task[] }
```

- If `task_ids` is omitted, returns all tasks where `parent_id` matches the calling peer.
- Returns full task records including status, result (if completed), child peer_id, and timestamps.

#### `cancel_task`

Requests cancellation of a delegated task. Sends a cancellation message to the worker and marks the task as `cancelled` in the broker.

```
cancel_task(
  task_id: string
) → { ok: bool }
```

- Sets the task status to `cancelled` in the broker.
- Sends a `send_message` to the worker peer with text indicating cancellation. The worker may or may not honor it (fire-and-forget agents will have already exited).
- Unblocks any pending `wait_for_result` calls — the task appears with status `cancelled`.
- Useful after `wait_for_result(mode: "any")` returns — cancel remaining tasks to free resources.

### Broker Changes

#### Tasks Table (In-Memory)

New in-memory store alongside peers, messages, and context:

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | 8-char hex, unique |
| parent_id | string | Peer ID of the orchestrator |
| child_id | string | Peer ID of the worker (set on spawn connect or immediately for request_task) |
| prompt | string | Task description |
| status | string | pending, completed, failed, cancelled |
| result | string | Worker's reported result |
| created_at | timestamp | Task creation time |
| completed_at | timestamp | When result was reported |

#### New Broker Endpoints

**`POST /task/create`**

Creates a new task. Called by the MCP server during `delegate` or `request_task`.

Request:
```json
{
  "parent_id": "abc123",
  "child_id": "def456",   // empty for delegate (set later), set for request_task
  "prompt": "Write tests for auth.go"
}
```

Response:
```json
{
  "task_id": "t_8f3a2b01"
}
```

**`POST /task/update`**

Updates task status and result. Called by worker's `report_result`.

Request:
```json
{
  "task_id": "t_8f3a2b01",
  "child_id": "def456",
  "status": "completed",
  "result": "All 12 tests pass. Coverage: 94%."
}
```

When a worker calls `report_result`, if the task has no `child_id` yet, the broker sets it from the caller's peer ID before applying the status update. This is a single atomic operation — no two-phase handshake. When the cleaner detects a dead peer with active tasks, it sets status to `failed`.

**`POST /task/wait`**

Long-poll endpoint. Blocks until requested tasks reach terminal state or timeout.

Request:
```json
{
  "task_ids": ["t_8f3a2b01", "t_9c4d3e02"],
  "mode": "all",
  "timeout_seconds": 300
}
```

Response (when tasks complete or timeout):
```json
{
  "results": [
    {"task_id": "t_8f3a2b01", "status": "completed", "result": "...", "peer_id": "def456", "completed_at": "..."},
    {"task_id": "t_9c4d3e02", "status": "running", "result": null, "peer_id": "ghi789", "completed_at": null}
  ],
  "timed_out": false
}
```

Implementation: broker holds a map of `task_id → []chan struct{}`. When a task update arrives, it closes the relevant channels, unblocking all waiters. Waiters use `select` with a timer for timeout.

**`POST /task/list`**

Returns tasks matching filter criteria. Called by `list_tasks`.

Request:
```json
{
  "parent_id": "abc123",
  "task_ids": ["t_8f3a2b01"]
}
```

Response:
```json
{
  "tasks": [
    {"task_id": "t_8f3a2b01", "parent_id": "abc123", "child_id": "def456", "status": "completed", "result": "...", "created_at": "...", "completed_at": "..."}
  ]
}
```

Either `parent_id` or `task_ids` can be used to filter. If both provided, `task_ids` takes precedence.

**`POST /task/cancel`**

Marks a task as cancelled. Called by `cancel_task`.

Request:
```json
{
  "task_id": "t_8f3a2b01"
}
```

Sets task status to `cancelled`, unblocks any waiters, and returns `{ "ok": true }`. The MCP server is responsible for sending the cancellation message to the worker peer.

#### Failure Detection

The existing peer cleaner (runs every 30s, removes peers stale for 60s or with dead PIDs) is extended:

- When a peer is cleaned up, find all tasks where `child_id` matches the dead peer and `status` is `pending` or `running`.
- Set those tasks to `failed` with result `"worker process exited unexpectedly"`.
- This unblocks any `wait_for_result` calls from the parent.

### Spawn Prompt Changes

`buildSpawnPrompt` gains a `taskID` parameter for delegated tasks:

**Fire-and-forget (no task tracking):**
```
You were spawned by agentswarm peer {parent_id}. When you finish your task, 
send your results back to that peer using send_message.

Your task:
{prompt}
```

**Delegated (with task tracking):**
```
You were spawned by agentswarm peer {parent_id} to complete task {task_id}.

When you finish, call report_result("{task_id}", "your result summary").
If you encounter an error you cannot resolve, call 
report_result("{task_id}", "description of failure", "failed").

Your task:
{prompt}
```

### Channel Message Format for request_task

When `request_task` sends to an existing peer, the channel message includes the task_id in metadata so the receiving agent knows to call `report_result`:

```
<channel source="agentswarm" task_id="t_8f3a2b01">
[request from abc123] Task t_8f3a2b01: Please review the auth module for security issues.
</channel>
```

The MCP server instructions are updated to tell agents: "When you receive a request with a task_id, call report_result with that task_id when you're done."

### Orchestration Patterns

These patterns emerge from the tool primitives — they are not built-in features.

**Fan-out / Fan-in:**
Orchestrator calls `delegate` N times, then `wait_for_result(all_ids, "all")` to collect all results. Reasons about combined output and takes next action.

**Sequential Pipeline:**
Orchestrator calls `delegate(task_A)` → `wait_for_result` → uses result to formulate `delegate(task_B)` → `wait_for_result` → done.

**Fan-out with Early Exit:**
Orchestrator calls `delegate` N times, then `wait_for_result(all_ids, "any")`. First good result wins.

**Hybrid with Human Peers:**
Orchestrator delegates automated work, waits for results, then uses `request_task` to send results to a human-started session for review. Waits for human peer's `report_result`, then continues.

**Dynamic Orchestration:**
The orchestrator is a full Claude Code session. It can read the codebase, check git status, run tests, and decide what to delegate next based on actual project state. This is what static workflow engines cannot do.

## What Changes

| Component | Change |
|-----------|--------|
| `internal/types/types.go` | Add Task, TaskResult, and request/response types |
| `internal/broker/store.go` | Add tasks map, CRUD operations, waiter channels |
| `internal/broker/broker.go` | Add /task/create, /task/update, /task/wait routes; extend cleaner |
| `internal/server/mcp.go` | Add 7 new tools (wait_for_messages, delegate, request_task, report_result, wait_for_result, list_tasks, cancel_task); keep check_messages as alias; update instructions |
| `internal/server/spawn.go` | Extend buildSpawnPrompt for task_id; add delegate-specific spawn logic |
| `README.md` | Full rewrite. Remove references to deleted binaries (agentswarm-broker, agentswarm CLI), fix install to single binary, replace SQLite with in-memory in architecture diagram, update MCP tools table (add set_name, whoami, spawn_agent + all orchestration tools), remove CLI section, fix env vars (remove AGENTSWARM_DB), add orchestration usage examples |
| `SPEC.md` | Full rewrite. Fix language references (Go not TypeScript/Bun), remove SQLite references, fix file structure to match actual layout (single cmd/, store.go not db.go, no cli/), remove threading references, add spawn_agent and orchestration sections, update all endpoint documentation |
| `CLAUDE.md` | Full rewrite. Fix architecture (single cmd/agentswarm-server/, no cli/), fix build instructions (single binary), update key files table (add store.go, spawn.go; remove db.go), fix dependencies (remove sqlite), add orchestration tools to conventions |

## What Doesn't Change

- Broker architecture (in-process, in-memory, single binary)
- SSE transport
- Existing tools (list_peers, send_message, broadcast, set_summary, set_name, whoami, get/set_context, spawn_agent, check_messages as alias)
- Peer registration and heartbeat
- Dependencies (pure Go, no new packages)

## Release & Install

### Install Methods

Three install paths, all backed by the same prebuilt binaries:

**1. GitHub Releases (foundation)**
Prebuilt binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64. Users download and put in PATH.

**2. Homebrew tap**
```bash
brew install scott-pallas/tap/agentswarm
```
Tap repo (`homebrew-tap`) with a formula that downloads the GitHub Release binary. Updated automatically on each release.

**3. Shell installer**
```bash
curl -fsSL https://raw.githubusercontent.com/scott-pallas/agentswarm/main/install.sh | sh
```
Detects OS/arch, downloads the right binary from GitHub Releases, installs to `~/.local/bin` (creates directory if needed, warns if not in PATH) or `/usr/local/bin` with sudo.

### Release Process (semi-automated)

**You control when:** Tag a release manually with `git tag v0.2.0 && git push --tags`.

**Everything after is automated via GitHub Actions:**

1. Tag push triggers `.github/workflows/release.yml`
2. Workflow builds `agentswarm-server` for all 4 platform/arch combos using `GOOS`/`GOARCH`
3. Creates GitHub Release with changelog (from commits since last tag)
4. Uploads binaries as release assets
5. Updates Homebrew tap formula with new version + checksums
6. Runs `go test ./...` as a gate before publishing

**Versioning:** Semantic versioning. You pick the version when you tag. No auto-bumping.

### New Files

| File | Purpose |
|------|---------|
| `.github/workflows/release.yml` | Build + publish on tag push |
| `.github/workflows/ci.yml` | Run tests on every push/PR |
| `install.sh` | Shell installer script |
| `scott-pallas/homebrew-tap` (separate repo) | Homebrew formula |

## Estimated Scope

~500-600 lines of new Go code for orchestration (7 tools + broker endpoints + store methods). No new Go dependencies. Release infrastructure is GitHub Actions YAML + shell script, outside the Go codebase.
