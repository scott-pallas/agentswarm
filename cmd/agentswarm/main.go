// Command agentswarm is a CLI utility for managing and debugging agentswarm.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/scott-pallas/agentswarm/internal/types"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	port := envOrDefault("AGENTSWARM_PORT", "7899")
	brokerURL := fmt.Sprintf("http://localhost:%s", port)

	switch os.Args[1] {
	case "status":
		cmdStatus(brokerURL)
	case "peers":
		cmdPeers(brokerURL)
	case "send":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: agentswarm send <peer_id> <message>")
			os.Exit(1)
		}
		cmdSend(brokerURL, os.Args[2], strings.Join(os.Args[3:], " "))
	case "broadcast":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: agentswarm broadcast <message>")
			os.Exit(1)
		}
		cmdBroadcast(brokerURL, strings.Join(os.Args[2:], " "))
	case "context":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: agentswarm context [list|get <key>|set <key> <value>]")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "list":
			cmdContextList(brokerURL)
		case "get":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "usage: agentswarm context get <key>")
				os.Exit(1)
			}
			cmdContextGet(brokerURL, os.Args[3])
		case "set":
			if len(os.Args) < 5 {
				fmt.Fprintln(os.Stderr, "usage: agentswarm context set <key> <value>")
				os.Exit(1)
			}
			cmdContextSet(brokerURL, os.Args[3], strings.Join(os.Args[4:], " "))
		default:
			fmt.Fprintf(os.Stderr, "unknown context command: %s\n", os.Args[2])
			os.Exit(1)
		}
	case "kill-broker":
		cmdKillBroker(brokerURL)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `agentswarm — CLI utility for agentswarm

Commands:
  status                     Broker status + peer count
  peers                      List all registered peers
  send <id> <message>        Send message to a peer
  broadcast <message>        Broadcast to all peers
  context list               Show all shared context
  context get <key>          Get a context value
  context set <key> <value>  Set a context value
  kill-broker                Stop the broker daemon`)
}

func cmdStatus(base string) {
	resp, err := http.Get(base + "/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "broker not reachable: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var h types.HealthResponse
	json.NewDecoder(resp.Body).Decode(&h)
	fmt.Printf("Status: %s\n", h.Status)
	fmt.Printf("Peers:  %d\n", h.Peers)
	fmt.Printf("Uptime: %ds\n", h.UptimeSeconds)
}

func cmdPeers(base string) {
	var peers []types.Peer
	if err := post(base+"/list-peers", types.ListPeersRequest{Scope: "machine"}, &peers); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(peers) == 0 {
		fmt.Println("No peers registered.")
		return
	}
	for _, p := range peers {
		fmt.Printf("  [%s] pid=%d cwd=%s branch=%s\n", p.ID, p.PID, p.CWD, p.GitBranch)
		if p.Summary != "" {
			fmt.Printf("         summary: %s\n", p.Summary)
		}
		if len(p.ActiveFiles) > 0 {
			fmt.Printf("         files: %s\n", strings.Join(p.ActiveFiles, ", "))
		}
	}
}

func cmdSend(base, peerID, msg string) {
	var resp types.SendResponse
	err := post(base+"/send", types.SendRequest{
		FromID: "cli",
		ToID:   peerID,
		Type:   types.TypeMessage,
		Text:   msg,
	}, &resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Sent message %d to %s\n", resp.MessageID, peerID)
}

func cmdBroadcast(base, msg string) {
	var resp types.BroadcastResponse
	err := post(base+"/broadcast", types.BroadcastRequest{
		FromID: "cli",
		Scope:  "machine",
		Type:   types.TypeBroadcast,
		Text:   msg,
	}, &resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Broadcast sent to %d peers\n", len(resp.SentTo))
}

func cmdContextList(base string) {
	var resp types.ContextListResponse
	if err := post(base+"/context/list", types.ContextListRequest{}, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(resp.Entries) == 0 {
		fmt.Println("No context entries.")
		return
	}
	for _, e := range resp.Entries {
		fmt.Printf("  %s = %s (set by %s at %s)\n", e.Key, e.Value, e.SetBy, e.UpdatedAt)
	}
}

func cmdContextGet(base, key string) {
	var resp types.ContextGetResponse
	if err := post(base+"/context/get", types.ContextGetRequest{Key: key}, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "not found: %s\n", key)
		os.Exit(1)
	}
	fmt.Printf("%s = %s\n", key, resp.Value)
	fmt.Printf("  set by: %s at %s\n", resp.SetBy, resp.UpdatedAt)
}

func cmdContextSet(base, key, value string) {
	if err := post(base+"/context/set", types.ContextSetRequest{
		PeerID: "cli", Key: key, Value: value,
	}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Set %s = %s\n", key, value)
}

func cmdKillBroker(base string) {
	// Just try a health check to confirm it's running, then there's no direct kill endpoint
	// In practice, you'd send SIGTERM to the broker process
	resp, err := http.Get(base + "/health")
	if err != nil {
		fmt.Println("Broker is not running.")
		return
	}
	resp.Body.Close()
	fmt.Println("To stop the broker, use: pkill agentswarm-broker")
}

// --- helpers ---

func post(url string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
