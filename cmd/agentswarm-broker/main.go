// Command agentswarm-broker runs the central coordination daemon.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/scott-pallas/agentswarm/internal/broker"
)

func main() {
	port := envOrDefault("AGENTSWARM_PORT", "7900")
	dbPath := os.Getenv("AGENTSWARM_DB") // empty = default (~/.agentswarm.db)
	staleTimeoutMs, _ := strconv.Atoi(envOrDefault("AGENTSWARM_STALE_TIMEOUT_MS", "60000"))

	db, err := broker.OpenDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	b := broker.New(db)
	b.StartCleaner(30*time.Second, time.Duration(staleTimeoutMs)*time.Millisecond)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: b.Handler(),
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		fmt.Printf("agentswarm broker listening on :%s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	fmt.Println("\nshutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
