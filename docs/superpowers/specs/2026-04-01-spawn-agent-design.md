# spawn_agent Tool Design

## Summary

Add a `spawn_agent` MCP tool that launches a new Claude Code process with a given task. The spawned agent auto-joins the agentswarm via the existing `.mcp.json` configuration and communicates results back through swarm messaging.

## Tool Interface

**Tool name:** `spawn_agent`

| Parameter | Type     | Required | Description                                      |
|-----------|----------|----------|--------------------------------------------------|
| `prompt`  | string   | yes      | Initial task/instruction for the new agent        |
| `cwd`     | string   | no       | Working directory (defaults to spawning peer's cwd) |
| `name`    | string   | no       | Display name for the spawned agent                |

**Returns immediately:**

```json
{
  "pid": 12345,
  "message": "Agent spawned. It will appear in list_peers once it connects to the swarm."
}
```

## Behavior

1. The tool launches a detached subprocess:
   ```
   claude --dangerously-skip-permissions --dangerously-load-development-channels server:agentspawn -p "<prompt>"
   ```
2. The working directory is set to `cwd` if provided, otherwise the current peer's working directory.
3. The prompt is augmented with:
   - The spawning peer's ID, so the child knows who to report back to.
   - If `name` is provided, an instruction to call `set_name("<name>")` on the first turn.
4. The spawned `claude` process picks up `.mcp.json` from the working directory, starts the agentswarm MCP server, and auto-registers with the broker.
5. The tool returns immediately with the child process PID. No blocking.
6. The spawned agent communicates results back via `send_message` to the parent peer ID included in its prompt.

## Prompt Augmentation

The user-provided prompt is wrapped with swarm context:

```
You were spawned by agentswarm peer <parent_peer_id>. When you finish your task, send your results back to that peer using send_message.

Your task:
<user_prompt>
```

If `name` is provided, prepend:

```
On your first turn, call set_name("<name>") to identify yourself in the swarm.
```

## Implementation Location

All changes are in `internal/server/mcp.go`:

- Add tool registration in `registerTools()` with the schema above.
- Add `handleSpawnAgent` method that:
  1. Reads `prompt`, `cwd`, `name` from arguments.
  2. Builds the augmented prompt string.
  3. Uses `os/exec.Command` to create the subprocess.
  4. Sets `cmd.Dir` to the resolved working directory.
  5. Detaches the process (sets `cmd.SysProcAttr` for background execution).
  6. Starts the process and returns the PID.

## Design Decisions

- **Fire-and-forget:** The tool returns immediately. No new infrastructure needed for tracking child lifecycle.
- **No spawn limits:** Rely on the user and OS to manage resources. Can add limits later if needed.
- **No kill tool:** Agents die naturally when `claude` exits. Can add management tools later.
- **Uses existing infrastructure:** Spawned agents join the swarm through the normal registration path. No special handling in the broker.
- **Prompt-based coordination:** The parent's peer ID is embedded in the prompt so the child knows where to send results. This avoids any new "parent-child" tracking in the broker.

## Testing

- Manual: spawn an agent, verify it appears in `list_peers`, verify it messages back when done.
- Unit: test prompt augmentation logic (string building, no subprocess needed).
