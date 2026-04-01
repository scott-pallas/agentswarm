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
