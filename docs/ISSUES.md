# Known Issues

Tracked issues for future work. File:line references are relative to the repo root.

---

## Should Fix

1. **ensureBroker hardcodes port :7900** -- `internal/server/mcp.go:339` uses `:7900` even though the port is configurable via `AGENTSWARM_PORT` env var. Should parse port from `brokerURL`.

2. **rand.Read errors discarded in ID generation** -- `internal/broker/broker.go:458` and `internal/broker/store.go:397` discard the error from `crypto/rand.Read`. Could produce zero IDs on entropy exhaustion.

3. **Discarded errors throughout** -- `json.Marshal` errors in broker SSE handler (`internal/broker/broker.go:103`, `internal/broker/broker.go:119`), `writeJSON` error in `internal/broker/broker.go:472`, heartbeat error in `internal/server/mcp.go:411`, send error in `internal/server/mcp.go:766` (`handleRequestTask`). All silently swallowed.

4. **No HTTP server timeouts** -- `internal/server/mcp.go:359` creates `http.Server` with no `ReadTimeout`, `WriteTimeout`, or `IdleTimeout`. Slowloris-style DoS possible on the broker port.

5. **Release workflow missing SHA256 checksums** -- `.github/workflows/release.yml` does not generate checksums for binaries. `install.sh` does not verify checksums. Supply chain concern.

6. **StartCleaner goroutine has no stop mechanism** -- `internal/broker/broker.go:62-79` ticker goroutine runs forever with no context or done channel. Leaks in tests.

7. **Spawn log files never cleaned up** -- `internal/server/spawn.go:49-68` creates temp files in `/tmp` that accumulate forever. No reaper or TTL.

8. **Unbounded goroutine spawning in SSE client** -- `internal/server/stream.go:105` fires `go c.onEvent(...)` for every SSE data line with no semaphore or worker pool.

9. **lookupPeer fetches all peers to find one** -- `internal/server/mcp.go:886` calls `/list-peers` with machine scope and linearly scans. A dedicated `/peer/:id` endpoint would be O(1).

10. **SPEC.md MCP instructions missing rules 7-9** -- SPEC.md only documents rules 1-6 but the code has 9 rules including orchestration guidance (`delegate`, `wait_for_result`, `cancel_task`).

11. **wait_for_messages missing from SPEC.md tool-by-tool section** -- Only appears in the orchestration tools table, not documented individually with parameters.

12. **SPEC.md check_messages description outdated** -- Does not mention it is now an alias for `wait_for_messages` with timeout 0.

13. **README missing "verify it works" section** -- No smoke test instructions after install.

14. **SPEC.md file structure missing install.sh, .github/, .mcp.json** -- File tree in SPEC.md does not reflect the current repo layout.

15. **Makefile install target needs sudo** -- `cp` to `/usr/local/bin/` fails without sudo on most systems. Should document or handle this.

---

## Nice to Have

1. **Broker authentication** -- No auth on the HTTP API. Any local process can impersonate peers, send messages, or cancel tasks. Consider a shared secret or per-peer token.

2. **Spawn mechanism security** -- `--dangerously-skip-permissions` gives spawned agents full system access. Consider scoping permissions or documenting the risk prominently.

3. **Prompt injection in spawn** -- User-supplied prompt, name, and taskID are interpolated directly into spawned agent prompts with no sanitization.

4. **No TLS** -- All broker communication is plaintext HTTP. Acceptable for localhost but risky if the port is exposed to the network.

5. **CWD parameter allows directory traversal** -- `spawn_agent`/`delegate` `cwd` argument is used as-is. Combined with `--dangerously-skip-permissions`, any peer can access files anywhere on disk.

6. **8-byte peer/task IDs** -- Only 32 bits of entropy (4 random bytes hex-encoded). Trivially brute-forceable for ID guessing attacks.

7. **isProcessAlive uses Signal(nil)** -- `internal/broker/store.go:211-213` sends `os.Signal(nil)` which is platform-dependent. Should use `syscall.Signal(0)` for portable liveness checks.

8. **envOrDefault duplicated** -- Defined in both `cmd/agentswarm-server/main.go` and `internal/server/mcp.go`. Extract to a shared utility.

---

## Test Coverage Gaps

Current coverage is approximately 15-20%. Only task store operations and spawn prompt building are tested.

### Priority 1: Broker HTTP Handlers (`internal/broker/broker.go` -- 0% coverage)

- Register, Unregister, Heartbeat, Health
- SetSummary, SetName, ListPeers (scope filtering, `exclude_id`)
- Send, Broadcast
- Context set/get/list
- Task create/update/wait/list/cancel
- Invalid JSON handling for all endpoints

### Priority 2: Store Peer/Message/Context Operations (`internal/broker/store.go` -- 0% for non-task ops)

- InsertAndGetPeer, DeletePeer, SetName, SetSummary
- UpdateHeartbeat, ListPeers scope filtering
- InsertMessage, UndeliveredMessages, MarkDelivered
- SetAndGetContext, ListContext scope filtering
- CleanStalePeers
- Concurrent access tests (race detector)

### Priority 3: SSE Manager (`internal/broker/sse.go` -- 0% coverage)

- Subscribe, Unsubscribe, Push, Broadcast, IsConnected
- Channel replacement on re-subscribe
- Full channel behavior (drop on full)
- Idempotent unsubscribe

### Priority 4: MCP Tool Handlers (`internal/server/mcp.go` -- 0% coverage)

- All 18 handlers via httptest + real broker
- `wait_for_messages` blocking/non-blocking/timeout
- `delegate` (mock `spawnClaude`)
- SSE event parsing for all event types

### Priority 5: Existing Test Gaps

- `WaitForTasks` with multiple tasks in "all" mode
- `UpdateTask` not-found error path
- `FailTasksForPeer` only fails pending tasks, not completed ones
- `BuildSpawnPrompt` with both name and taskID provided
