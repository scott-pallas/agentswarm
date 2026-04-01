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
