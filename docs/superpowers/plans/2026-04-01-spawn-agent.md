# spawn_agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `spawn_agent` MCP tool that launches a new Claude Code process as a detached subprocess, auto-joining the agentswarm.

**Architecture:** Single new tool handler in `internal/server/mcp.go` that builds an augmented prompt, launches `claude ... server:agentspawn -p "<prompt>"` via `os/exec`, and returns the PID. No broker changes needed — the spawned agent joins via existing registration.

**Tech Stack:** Go, os/exec, existing MCP tool registration pattern

---

### File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/server/mcp.go` | Modify | Add `spawn_agent` tool registration + `handleSpawnAgent` handler |
| `internal/server/spawn.go` | Create | `buildSpawnPrompt` helper (prompt augmentation logic) and `spawnClaude` helper (subprocess launch) |
| `internal/server/spawn_test.go` | Create | Unit tests for prompt building logic |

---

### Task 1: Prompt augmentation helper

**Files:**
- Create: `internal/server/spawn_test.go`
- Create: `internal/server/spawn.go`

- [ ] **Step 1: Write the failing test for buildSpawnPrompt (no name)**

```go
package server

import "testing"

func TestBuildSpawnPrompt_NoName(t *testing.T) {
	got := buildSpawnPrompt("Fix the login bug", "abc123", "")
	expected := "You were spawned by agentswarm peer abc123. " +
		"When you finish your task, send your results back to that peer using send_message.\n\n" +
		"Your task:\nFix the login bug"
	if got != expected {
		t.Errorf("buildSpawnPrompt(no name):\ngot:  %q\nwant: %q", got, expected)
	}
}
```

- [ ] **Step 2: Write the failing test for buildSpawnPrompt (with name)**

Add to the same file:

```go
func TestBuildSpawnPrompt_WithName(t *testing.T) {
	got := buildSpawnPrompt("Fix the login bug", "abc123", "BugFixer")
	expected := "On your first turn, call set_name(\"BugFixer\") to identify yourself in the swarm.\n\n" +
		"You were spawned by agentswarm peer abc123. " +
		"When you finish your task, send your results back to that peer using send_message.\n\n" +
		"Your task:\nFix the login bug"
	if got != expected {
		t.Errorf("buildSpawnPrompt(with name):\ngot:  %q\nwant: %q", got, expected)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/server/ -run TestBuildSpawnPrompt -v`
Expected: FAIL — `buildSpawnPrompt` not defined

- [ ] **Step 4: Implement buildSpawnPrompt in spawn.go**

```go
package server

import "fmt"

// buildSpawnPrompt wraps a user prompt with swarm context for a spawned agent.
func buildSpawnPrompt(userPrompt, parentPeerID, name string) string {
	var prompt string
	if name != "" {
		prompt = fmt.Sprintf("On your first turn, call set_name(%q) to identify yourself in the swarm.\n\n", name)
	}
	prompt += fmt.Sprintf(
		"You were spawned by agentswarm peer %s. When you finish your task, send your results back to that peer using send_message.\n\nYour task:\n%s",
		parentPeerID, userPrompt,
	)
	return prompt
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/server/ -run TestBuildSpawnPrompt -v`
Expected: PASS (both tests)

- [ ] **Step 6: Commit**

```bash
git add internal/server/spawn.go internal/server/spawn_test.go
git commit -m "feat: add buildSpawnPrompt helper for spawn_agent tool"
```

---

### Task 2: Subprocess launch helper

**Files:**
- Modify: `internal/server/spawn.go`

- [ ] **Step 1: Add spawnClaude function to spawn.go**

```go
import (
	"fmt"
	"os/exec"
	"syscall"
)

// spawnClaude launches a detached claude process with the given prompt and working directory.
// Returns the process PID.
func spawnClaude(prompt, cwd string) (int, error) {
	cmd := exec.Command(
		"claude",
		"--dangerously-skip-permissions",
		"--dangerously-load-development-channels",
		"server:agentspawn",
		"-p", prompt,
	)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	// Discard stdout/stderr — the spawned agent communicates via the swarm
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to spawn claude: %w", err)
	}

	// Detach — don't wait for the process
	go cmd.Wait()

	return cmd.Process.Pid, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/server/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/server/spawn.go
git commit -m "feat: add spawnClaude subprocess launcher"
```

---

### Task 3: Register spawn_agent tool and handler

**Files:**
- Modify: `internal/server/mcp.go`

- [ ] **Step 1: Add tool registration in registerTools()**

Add after the `check_messages` tool registration (around line 180):

```go
	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "spawn_agent",
		Description: "Launch a new Claude Code agent that joins the swarm. Fire-and-forget: returns immediately with the PID.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"prompt": map[string]interface{}{"type": "string", "description": "The task/instruction for the new agent"},
				"cwd":    map[string]interface{}{"type": "string", "description": "Working directory for the agent (defaults to current peer's cwd)"},
				"name":   map[string]interface{}{"type": "string", "description": "Display name for the spawned agent"},
			},
			Required: []string{"prompt"},
		},
	}, s.handleSpawnAgent)
```

- [ ] **Step 2: Add handleSpawnAgent method**

Add after `handleCheckMessages`:

```go
func (s *MCPServer) handleSpawnAgent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	prompt, _ := args["prompt"].(string)
	cwd, _ := args["cwd"].(string)
	name, _ := args["name"].(string)

	if cwd == "" {
		cwd = s.peerCtx.CWD
	}

	augmented := buildSpawnPrompt(prompt, s.peerID, name)

	pid, err := spawnClaude(augmented, cwd)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Agent spawned (pid: %d). It will appear in list_peers once it connects to the swarm.",
		pid,
	)), nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add internal/server/mcp.go
git commit -m "feat: add spawn_agent MCP tool"
```

---

### Task 4: Build, install, and manual smoke test

**Files:** None (verification only)

- [ ] **Step 1: Build the project**

Run: `make build`
Expected: clean build, binaries in `bin/`

- [ ] **Step 2: Install**

Run: `make install`
Expected: binaries copied to `/usr/local/bin/`

- [ ] **Step 3: Manual smoke test**

From a Claude Code session with agentswarm connected, run:
1. Call `spawn_agent` with `prompt: "Call whoami and then send me a message with your peer ID"` and `name: "SmokeTest"`
2. Verify `list_peers` shows the new agent within a few seconds
3. Verify you receive a message back from the spawned agent

- [ ] **Step 4: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "fix: smoke test fixes for spawn_agent"
```
