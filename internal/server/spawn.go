package server

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// buildSpawnPrompt wraps a user prompt with swarm context for a spawned agent.
func buildSpawnPrompt(userPrompt, parentPeerID, name string, interactive bool) string {
	var prompt string
	if name != "" {
		prompt = fmt.Sprintf("On your first turn, call set_name(%q) to identify yourself in the swarm.\n\n", name)
	}
	prompt += fmt.Sprintf(
		"You were spawned by agentswarm peer %s.",
		parentPeerID,
	)
	if interactive {
		prompt += " You are in INTERACTIVE mode — you must stay alive and respond to all incoming messages. Do NOT exit or stop. When you receive a channel message, respond immediately using send_message."
	} else {
		prompt += " When you finish your task, send your results back to that peer using send_message."
	}
	prompt += fmt.Sprintf("\n\nYour task:\n%s", userPrompt)
	return prompt
}

// spawnClaude launches a detached claude process with the given prompt and working directory.
// Always uses -p to pass the initial prompt. For interactive agents, the process stays alive
// as long as the swarm keeps delivering messages via the MCP server's SSE channel.
// Returns the process PID and log file path.
func spawnClaude(prompt, cwd string, interactive bool) (int, string, error) {
	args := []string{
		"--dangerously-skip-permissions",
		"--dangerously-load-development-channels",
		"server:agentspawn",
		"-p", prompt,
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// Log stdout/stderr to a temp file for debugging
	logFile, err := os.CreateTemp("", fmt.Sprintf("agentswarm-spawn-%d-*.log", os.Getpid()))
	if err != nil {
		return 0, "", fmt.Errorf("failed to create spawn log: %w", err)
	}
	logPath := logFile.Name()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, "", fmt.Errorf("failed to spawn claude: %w", err)
	}

	// Close log file when process exits
	go func() {
		cmd.Wait()
		logFile.Close()
	}()

	return cmd.Process.Pid, logPath, nil
}
