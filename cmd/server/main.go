// Command agentswarm-server runs the MCP server for a single Claude Code session.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/scott-pallas/agentswarm/internal/server"
)

func main() {
	port := envOrDefault("AGENTSWARM_PORT", "7899")
	brokerURL := fmt.Sprintf("http://localhost:%s", port)

	// Auto-launch broker if not running
	if err := server.EnsureBroker(brokerURL); err != nil {
		log.Fatalf("broker unavailable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	srv := server.NewMCPServer(brokerURL)
	defer srv.Shutdown()

	go func() {
		<-sigCh
		log.Println("shutting down MCP server...")
		cancel()
	}()

	if err := srv.Start(ctx); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
