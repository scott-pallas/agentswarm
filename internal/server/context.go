// Package server implements the MCP server that connects Claude Code to the broker.
package server

import (
	"os"
	"os/exec"
	"strings"
)

// PeerContext holds the local environment info for this Claude Code session.
type PeerContext struct {
	CWD         string
	GitRoot     string
	GitBranch   string
	TTY         string
	ActiveFiles []string
}

// DetectContext gathers git, CWD, TTY, and active files info.
func DetectContext() *PeerContext {
	ctx := &PeerContext{}

	ctx.CWD, _ = os.Getwd()
	ctx.GitRoot = gitCmd("rev-parse", "--show-toplevel")
	ctx.GitBranch = gitCmd("rev-parse", "--abbrev-ref", "HEAD")
	ctx.TTY = os.Getenv("TTY")
	if ctx.TTY == "" {
		// Try to detect from /dev/tty
		if fi, err := os.Stdin.Stat(); err == nil {
			if fi.Mode()&os.ModeCharDevice != 0 {
				ctx.TTY = os.Stdin.Name()
			}
		}
	}
	ctx.ActiveFiles = detectActiveFiles(ctx.GitRoot)

	return ctx
}

// RefreshActiveFiles updates the active files list.
func (c *PeerContext) RefreshActiveFiles() {
	c.ActiveFiles = detectActiveFiles(c.GitRoot)
	c.GitBranch = gitCmd("rev-parse", "--abbrev-ref", "HEAD")
}

func detectActiveFiles(gitRoot string) []string {
	if gitRoot == "" {
		return []string{}
	}
	out := gitCmd("diff", "--name-only")
	staged := gitCmd("diff", "--name-only", "--cached")

	files := make(map[string]bool)
	for _, f := range splitLines(out) {
		if f != "" {
			files[f] = true
		}
	}
	for _, f := range splitLines(staged) {
		if f != "" {
			files[f] = true
		}
	}

	result := make([]string, 0, len(files))
	for f := range files {
		result = append(result, f)
	}
	return result
}

func gitCmd(args ...string) string {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
