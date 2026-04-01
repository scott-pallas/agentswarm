package server

import (
	"fmt"
	"os/exec"
	"syscall"
)

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
