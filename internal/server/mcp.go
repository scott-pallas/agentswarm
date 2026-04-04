package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/scott-pallas/agentswarm/internal/broker"
	"github.com/scott-pallas/agentswarm/internal/types"
)

// MCPServer wraps the MCP stdio server and broker communication.
type MCPServer struct {
	brokerURL  string
	peerID     string
	peerCtx    *PeerContext
	mcpSrv     *mcpserver.MCPServer
	sseClient  *SSEClient
	httpServer *http.Server // non-nil if this instance is the broker
	httpClient *http.Client
	msgChan    chan string // buffered channel for incoming messages from SSE
}

// NewMCPServer creates and configures the MCP server with all tools.
func NewMCPServer(brokerURL string) *MCPServer {
	s := &MCPServer{
		brokerURL:  brokerURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		msgChan:    make(chan string, 64),
	}

	hooks := &mcpserver.Hooks{}
	hooks.AddAfterInitialize(func(ctx context.Context, id any, msg *mcp.InitializeRequest, result *mcp.InitializeResult) {
		if result.Capabilities.Experimental == nil {
			result.Capabilities.Experimental = make(map[string]interface{})
		}
		result.Capabilities.Experimental["claude/channel"] = map[string]interface{}{}
	})

	s.mcpSrv = mcpserver.NewMCPServer(
		"agentswarm",
		"0.1.0",
		mcpserver.WithHooks(hooks),
		mcpserver.WithInstructions(`You are connected to agentswarm. Other Claude Code instances can see you and message you.

RULES:
1. When you receive a <channel source="agentswarm"> message, RESPOND IMMEDIATELY.
   Pause your current work, reply using send_message, then resume.
2. On your first turn, call set_summary to describe what you're working on.
3. Before editing a shared file, call list_peers to check if anyone else is working on it.
4. Use get_context/set_context to share API contracts, decisions, or schemas with peers.
5. Use broadcast for announcements that affect everyone (refactors, breaking changes).
6. Use appropriate message types:
   - "question" when you need a response
   - "alert" for urgent conflicts or breaking changes
   - "notification" for FYI status updates
   - "request" when delegating a task to another peer
7. Use delegate to spawn tracked agents. Use wait_for_result to collect their output.
8. When you receive a request with a task_id, call report_result with that task_id when you're done.
9. Use cancel_task after wait_for_result(mode: "any") to clean up remaining agents.`),
	)

	s.registerTools()
	return s
}

func (s *MCPServer) registerTools() {
	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "list_peers",
		Description: "Discover other Claude Code instances. Shows what they're working on and what files they're editing.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"scope": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"machine", "directory", "repo"},
					"description": "Discovery scope",
				},
			},
			Required: []string{"scope"},
		},
	}, s.handleListPeers)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "send_message",
		Description: "Send a message to another Claude Code instance. Arrives instantly.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"to_id": map[string]interface{}{"type": "string", "description": "Target peer ID"},
				"text":  map[string]interface{}{"type": "string", "description": "Message content"},
				"type":  map[string]interface{}{"type": "string", "enum": []string{"message", "question", "request", "alert", "notification"}, "description": "Message type"},
				"files": map[string]interface{}{"type": "array", "description": "Relevant file paths for context (optional)"},
			},
			Required: []string{"to_id", "text"},
		},
	}, s.handleSendMessage)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "broadcast",
		Description: "Send a message to all peers in a scope (repo, directory, or machine).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"text":  map[string]interface{}{"type": "string", "description": "Message content"},
				"scope": map[string]interface{}{"type": "string", "enum": []string{"machine", "directory", "repo"}, "description": "Broadcast scope"},
				"type":  map[string]interface{}{"type": "string", "enum": []string{"message", "alert", "notification"}, "description": "Message type"},
			},
			Required: []string{"text", "scope"},
		},
	}, s.handleBroadcast)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "set_summary",
		Description: "Describe what you're working on (visible to other peers).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"summary": map[string]interface{}{"type": "string", "description": "1-2 sentence summary"},
			},
			Required: []string{"summary"},
		},
	}, s.handleSetSummary)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "set_name",
		Description: "Set a human-readable name for this peer (visible to other peers).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Display name for this peer"},
			},
			Required: []string{"name"},
		},
	}, s.handleSetName)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "whoami",
		Description: "Returns your own peer ID, name, and registration info.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleWhoAmI)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "get_context",
		Description: "Read a shared context value set by any peer in the same scope.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key": map[string]interface{}{"type": "string", "description": "Context key to read"},
			},
			Required: []string{"key"},
		},
	}, s.handleGetContext)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "set_context",
		Description: "Set a shared context value visible to all peers in the same repo/directory.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"key":   map[string]interface{}{"type": "string", "description": "Context key"},
				"value": map[string]interface{}{"type": "string", "description": "Context value (can be JSON stringified)"},
			},
			Required: []string{"key", "value"},
		},
	}, s.handleSetContext)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "wait_for_messages",
		Description: "Block until messages arrive for this peer. Use timeout_seconds: 0 for non-blocking check.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"timeout_seconds": map[string]interface{}{"type": "number", "description": "How long to wait. Default 120s. 0 = non-blocking."},
			},
		},
	}, s.handleWaitForMessages)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "check_messages",
		Description: "Manually check for messages (backward-compatible alias for wait_for_messages with timeout 0).",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		req.Params.Arguments = map[string]interface{}{"timeout_seconds": float64(0)}
		return s.handleWaitForMessages(ctx, req)
	})

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "spawn_agent",
		Description: "Launch a new Claude Code agent that joins the swarm. Returns immediately with the PID.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"prompt": map[string]interface{}{"type": "string", "description": "The task/instruction for the new agent"},
				"cwd":    map[string]interface{}{"type": "string", "description": "Working directory for the agent (defaults to current peer's cwd)"},
				"name":   map[string]interface{}{"type": "string", "description": "Display name for the spawned agent"},
				"mode":   map[string]interface{}{"type": "string", "enum": []string{"fire-and-forget", "interactive"}, "description": "fire-and-forget (default): agent runs the task and exits. interactive: agent stays alive for multi-turn message exchange."},
			},
			Required: []string{"prompt"},
		},
	}, s.handleSpawnAgent)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "delegate",
		Description: "Spawn a new agent with a tracked task. Returns task_id immediately.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"prompt": map[string]interface{}{"type": "string", "description": "Task for the new agent"},
				"name":   map[string]interface{}{"type": "string", "description": "Display name for the agent"},
				"cwd":    map[string]interface{}{"type": "string", "description": "Working directory (defaults to current)"},
			},
			Required: []string{"prompt"},
		},
	}, s.handleDelegate)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "request_task",
		Description: "Assign a tracked task to an existing peer.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"peer_id": map[string]interface{}{"type": "string", "description": "Target peer ID"},
				"prompt":  map[string]interface{}{"type": "string", "description": "Task description"},
			},
			Required: []string{"peer_id", "prompt"},
		},
	}, s.handleRequestTask)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "report_result",
		Description: "Report completion of a delegated task.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"task_id": map[string]interface{}{"type": "string", "description": "The task ID to report on"},
				"result":  map[string]interface{}{"type": "string", "description": "Result summary"},
				"status":  map[string]interface{}{"type": "string", "enum": []string{"completed", "failed"}, "description": "Task outcome (default: completed)"},
			},
			Required: []string{"task_id", "result"},
		},
	}, s.handleReportResult)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "wait_for_result",
		Description: "Block until delegated task(s) complete, fail, or timeout.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"task_id":         map[string]interface{}{"description": "Task ID or array of task IDs"},
				"mode":            map[string]interface{}{"type": "string", "enum": []string{"any", "all"}, "description": "Wait mode (default: all)"},
				"timeout_seconds": map[string]interface{}{"type": "number", "description": "Timeout in seconds (default: 300)"},
			},
			Required: []string{"task_id"},
		},
	}, s.handleWaitForResult)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "list_tasks",
		Description: "Check status of delegated tasks without blocking.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"task_ids": map[string]interface{}{"type": "array", "description": "Specific task IDs to check (omit for all your tasks)"},
			},
		},
	}, s.handleListTasks)

	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "cancel_task",
		Description: "Cancel a delegated task and notify the worker.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"task_id": map[string]interface{}{"type": "string", "description": "Task ID to cancel"},
			},
			Required: []string{"task_id"},
		},
	}, s.handleCancelTask)
}

// Start launches the MCP stdio server immediately, then connects to the
// broker in the background.
func (s *MCPServer) Start(ctx context.Context) error {
	go s.connectToBroker(ctx)

	stdio := mcpserver.NewStdioServer(s.mcpSrv)
	return stdio.Listen(ctx, os.Stdin, os.Stdout)
}

// connectToBroker tries to connect to an existing broker, or becomes the broker.
func (s *MCPServer) connectToBroker(ctx context.Context) {
	if err := s.ensureBroker(); err != nil {
		log.Printf("broker unavailable: %v", err)
		return
	}

	s.peerCtx = DetectContext()

	if err := s.register(); err != nil {
		log.Printf("failed to register with broker: %v", err)
		return
	}
	log.Printf("registered as peer %s", s.peerID)

	s.sseClient = NewSSEClient(s.brokerURL, s.peerID, s.handleSSEEvent)
	s.sseClient.Start()

	go s.heartbeatLoop(ctx)
}

// ensureBroker checks if a broker is running. If not, starts one in-process.
func (s *MCPServer) ensureBroker() error {
	// Check if broker already exists
	resp, err := http.Get(s.brokerURL + "/health")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return nil
		}
	}

	log.Println("no broker found, starting in-process...")

	u, err := url.Parse(s.brokerURL)
	if err != nil {
		return fmt.Errorf("invalid broker URL: %w", err)
	}
	host := u.Host
	_, port, _ := net.SplitHostPort(host)
	if port == "" {
		port = "7900"
	}

	// Try to bind the port — if it fails, another instance beat us to it
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		// Port taken — another instance just became the broker, wait for it
		log.Printf("port %s taken, connecting as client...", port)
		for i := 0; i < 20; i++ {
			time.Sleep(250 * time.Millisecond)
			resp, err := http.Get(s.brokerURL + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return nil
				}
			}
		}
		return fmt.Errorf("broker did not start in time")
	}

	b := broker.New()
	b.StartCleaner(30*time.Second, 60*time.Second)

	s.httpServer = &http.Server{
		Handler:      b.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE streams need unlimited write time
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("broker HTTP error: %v", err)
		}
	}()

	log.Printf("in-process broker started on :%s", port)
	return nil
}

// Shutdown cleans up broker registration and SSE.
func (s *MCPServer) Shutdown() {
	if s.sseClient != nil {
		s.sseClient.Stop()
	}
	if s.peerID != "" {
		s.brokerPost("/unregister", types.UnregisterRequest{ID: s.peerID}, nil)
	}
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

func (s *MCPServer) register() error {
	req := types.RegisterRequest{
		PID:         os.Getpid(),
		CWD:         s.peerCtx.CWD,
		GitRoot:     s.peerCtx.GitRoot,
		GitBranch:   s.peerCtx.GitBranch,
		TTY:         s.peerCtx.TTY,
		ActiveFiles: s.peerCtx.ActiveFiles,
	}
	var resp types.RegisterResponse
	if err := s.brokerPost("/register", req, &resp); err != nil {
		return err
	}
	s.peerID = resp.ID
	return nil
}

func (s *MCPServer) heartbeatLoop(ctx context.Context) {
	intervalMs, _ := strconv.Atoi(envOrDefault("AGENTSWARM_HEARTBEAT_MS", "15000"))
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.peerCtx.RefreshActiveFiles()
			if err := s.brokerPost("/heartbeat", types.HeartbeatRequest{
				ID:          s.peerID,
				ActiveFiles: s.peerCtx.ActiveFiles,
				GitBranch:   s.peerCtx.GitBranch,
			}, nil); err != nil {
				log.Printf("heartbeat failed: %v", err)
			}
		}
	}
}

func (s *MCPServer) handleSSEEvent(event, data string) {
	log.Printf("SSE event received: event=%s data=%s", event, data)
	var content string
	var meta map[string]interface{}

	switch event {
	case "message", "broadcast":
		var msg types.Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			return
		}
		content = fmt.Sprintf("[%s from %s] %s", msg.Type, msg.FromID, msg.Text)
		meta = map[string]interface{}{
			"from_id": msg.FromID,
		}
		// Best-effort peer enrichment with a short timeout to avoid blocking.
		done := make(chan struct{})
		go func() {
			defer close(done)
			if peer, err := s.lookupPeer(msg.FromID); err == nil {
				meta["from_summary"] = peer.Summary
				meta["from_cwd"] = peer.CWD
			}
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			log.Printf("lookupPeer timed out for %s, skipping enrichment", msg.FromID)
		}
	case "peer_joined":
		var pj types.PeerJoined
		if err := json.Unmarshal([]byte(data), &pj); err != nil {
			return
		}
		content = fmt.Sprintf("Peer %s joined (%s) — %s", pj.ID, pj.CWD, pj.Summary)
	case "peer_left":
		var pl types.PeerLeft
		if err := json.Unmarshal([]byte(data), &pl); err != nil {
			return
		}
		content = fmt.Sprintf("Peer %s left (%s)", pl.ID, pl.Reason)
	case "context_updated":
		content = fmt.Sprintf("Context updated: %s", data)
	default:
		content = fmt.Sprintf("[%s] %s", event, data)
	}

	params := map[string]interface{}{
		"content": content,
	}
	if meta != nil {
		params["meta"] = meta
	}
	s.mcpSrv.SendNotificationToAllClients("notifications/claude/channel", params)

	// Feed into message channel for wait_for_messages
	select {
	case s.msgChan <- content:
	default:
	}
}

// --- Tool handlers ---

func (s *MCPServer) handleListPeers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	scope, _ := req.Params.Arguments["scope"].(string)
	if scope == "" {
		scope = "machine"
	}

	brokerReq := types.ListPeersRequest{
		Scope:     scope,
		CWD:       s.peerCtx.CWD,
		GitRoot:   s.peerCtx.GitRoot,
		ExcludeID: s.peerID,
	}

	var peers []types.Peer
	if err := s.brokerPost("/list-peers", brokerReq, &peers); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	data, _ := json.MarshalIndent(peers, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) handleSendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	toID, _ := args["to_id"].(string)
	text, _ := args["text"].(string)
	msgType, _ := args["type"].(string)

	if msgType == "" {
		msgType = "message"
	}

	brokerReq := types.SendRequest{
		FromID: s.peerID,
		ToID:   toID,
		Type:   types.MessageType(msgType),
		Text:   text,
	}

	if files, ok := args["files"]; ok {
		if fileList, ok := files.([]interface{}); ok {
			mc := types.MessageContext{}
			for _, f := range fileList {
				if fs, ok := f.(string); ok {
					mc.Files = append(mc.Files, fs)
				}
			}
			brokerReq.Context, _ = json.Marshal(mc)
		}
	}

	var resp types.SendResponse
	if err := s.brokerPost("/send", brokerReq, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Message sent (id: %d)", resp.MessageID)), nil
}

func (s *MCPServer) handleBroadcast(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	text, _ := args["text"].(string)
	scope, _ := args["scope"].(string)
	msgType, _ := args["type"].(string)

	if msgType == "" {
		msgType = "notification"
	}

	brokerReq := types.BroadcastRequest{
		FromID:  s.peerID,
		Scope:   scope,
		Type:    types.MessageType(msgType),
		Text:    text,
		CWD:     s.peerCtx.CWD,
		GitRoot: s.peerCtx.GitRoot,
	}

	var resp types.BroadcastResponse
	if err := s.brokerPost("/broadcast", brokerReq, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Broadcast sent to %d peers: %v", len(resp.SentTo), resp.SentTo)), nil
}

func (s *MCPServer) handleSetSummary(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	summary, _ := req.Params.Arguments["summary"].(string)

	if err := s.brokerPost("/set-summary", types.SetSummaryRequest{
		ID: s.peerID, Summary: summary,
	}, nil); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText("Summary updated"), nil
}

func (s *MCPServer) handleSetName(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, _ := req.Params.Arguments["name"].(string)
	if err := s.brokerPost("/set-name", types.SetNameRequest{
		ID: s.peerID, Name: name,
	}, nil); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Name set to '%s'", name)), nil
}

func (s *MCPServer) handleWhoAmI(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.peerID == "" {
		return mcp.NewToolResultText("Not yet registered with broker (still connecting)"), nil
	}
	peer, err := s.lookupPeer(s.peerID)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Peer ID: %s (broker lookup failed: %v)", s.peerID, err)), nil
	}
	data, _ := json.MarshalIndent(peer, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) handleGetContext(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, _ := req.Params.Arguments["key"].(string)

	scopeType := "repo"
	scopeValue := s.peerCtx.GitRoot
	if scopeValue == "" {
		scopeType = "machine"
		scopeValue = "machine"
	}

	var resp types.ContextGetResponse
	if err := s.brokerPost("/context/get", types.ContextGetRequest{
		Key: key, Scope: scopeType, ScopeValue: scopeValue,
	}, &resp); err != nil {
		return mcp.NewToolResultError("not found: " + key), nil
	}

	data, _ := json.MarshalIndent(resp, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) handleSetContext(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, _ := req.Params.Arguments["key"].(string)
	value, _ := req.Params.Arguments["value"].(string)

	scopeType := "repo"
	scopeValue := s.peerCtx.GitRoot
	if scopeValue == "" {
		scopeType = "machine"
		scopeValue = "machine"
	}

	if err := s.brokerPost("/context/set", types.ContextSetRequest{
		PeerID: s.peerID, Key: key, Value: value,
		Scope: scopeType, ScopeValue: scopeValue,
	}, nil); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Context '%s' set", key)), nil
}

func (s *MCPServer) handleWaitForMessages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	timeoutSec := 120.0
	if t, ok := req.Params.Arguments["timeout_seconds"].(float64); ok {
		timeoutSec = t
	}

	if timeoutSec == 0 {
		var messages []string
		for {
			select {
			case msg := <-s.msgChan:
				messages = append(messages, msg)
			default:
				result := map[string]interface{}{"messages": messages, "timed_out": false}
				data, _ := json.MarshalIndent(result, "", "  ")
				return mcp.NewToolResultText(string(data)), nil
			}
		}
	}

	timeout := time.Duration(timeoutSec * float64(time.Second))
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var messages []string
	select {
	case msg := <-s.msgChan:
		messages = append(messages, msg)
	case <-timer.C:
		result := map[string]interface{}{"messages": []string{}, "timed_out": true}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	case <-ctx.Done():
		result := map[string]interface{}{"messages": []string{}, "timed_out": true, "cancelled": true}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	// Drain additional messages
	for {
		select {
		case msg := <-s.msgChan:
			messages = append(messages, msg)
		default:
			result := map[string]interface{}{"messages": messages, "timed_out": false}
			data, _ := json.MarshalIndent(result, "", "  ")
			return mcp.NewToolResultText(string(data)), nil
		}
	}
}

func (s *MCPServer) handleSpawnAgent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	prompt, _ := args["prompt"].(string)
	cwd, _ := args["cwd"].(string)
	name, _ := args["name"].(string)
	mode, _ := args["mode"].(string)

	if cwd == "" {
		cwd = s.peerCtx.CWD
	}

	interactive := mode == "interactive"
	augmented := buildSpawnPrompt(prompt, s.peerID, name, interactive, "")

	pid, logPath, err := spawnClaude(augmented, cwd, interactive)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	modeLabel := "fire-and-forget"
	if interactive {
		modeLabel = "interactive"
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Agent spawned (pid: %d, mode: %s). It will appear in list_peers once it connects to the swarm. Log: %s",
		pid, modeLabel, logPath,
	)), nil
}

func (s *MCPServer) handleDelegate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	prompt, _ := args["prompt"].(string)
	name, _ := args["name"].(string)
	cwd, _ := args["cwd"].(string)
	if cwd == "" {
		cwd = s.peerCtx.CWD
	}

	var taskResp types.TaskCreateResponse
	if err := s.brokerPost("/task/create", types.TaskCreateRequest{
		ParentID: s.peerID,
		Prompt:   prompt,
	}, &taskResp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	augmented := buildSpawnPrompt(prompt, s.peerID, name, false, taskResp.TaskID)
	pid, logPath, err := spawnClaude(augmented, cwd, false)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		`{"task_id": %q, "pid": %d, "log": %q}`,
		taskResp.TaskID, pid, logPath,
	)), nil
}

func (s *MCPServer) handleRequestTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	peerID, _ := args["peer_id"].(string)
	prompt, _ := args["prompt"].(string)

	var taskResp types.TaskCreateResponse
	if err := s.brokerPost("/task/create", types.TaskCreateRequest{
		ParentID: s.peerID,
		ChildID:  peerID,
		Prompt:   prompt,
	}, &taskResp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	if err := s.brokerPost("/send", types.SendRequest{
		FromID: s.peerID,
		ToID:   peerID,
		Type:   types.TypeRequest,
		Text:   fmt.Sprintf("Task %s: %s", taskResp.TaskID, prompt),
	}, nil); err != nil {
		log.Printf("failed to send task request to peer %s: %v", peerID, err)
	}

	return mcp.NewToolResultText(fmt.Sprintf(`{"task_id": %q}`, taskResp.TaskID)), nil
}

func (s *MCPServer) handleReportResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	taskID, _ := args["task_id"].(string)
	result, _ := args["result"].(string)
	status, _ := args["status"].(string)
	if status == "" {
		status = "completed"
	}

	if err := s.brokerPost("/task/update", types.TaskUpdateRequest{
		TaskID:  taskID,
		ChildID: s.peerID,
		Status:  status,
		Result:  result,
	}, nil); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(`{"ok": true}`), nil
}

func (s *MCPServer) handleWaitForResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments

	var taskIDs []string
	switch v := args["task_id"].(type) {
	case string:
		taskIDs = []string{v}
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				taskIDs = append(taskIDs, s)
			}
		}
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "all"
	}
	timeoutSec := 300.0
	if t, ok := args["timeout_seconds"].(float64); ok {
		timeoutSec = t
	}

	var resp types.TaskWaitResponse
	if err := s.brokerPost("/task/wait", types.TaskWaitRequest{
		TaskIDs:        taskIDs,
		Mode:           mode,
		TimeoutSeconds: int(timeoutSec),
	}, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	data, _ := json.MarshalIndent(resp, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) handleListTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments

	var taskIDs []string
	if ids, ok := args["task_ids"].([]interface{}); ok {
		for _, item := range ids {
			if id, ok := item.(string); ok {
				taskIDs = append(taskIDs, id)
			}
		}
	}

	listReq := types.TaskListRequest{TaskIDs: taskIDs}
	if len(taskIDs) == 0 {
		listReq.ParentID = s.peerID
	}

	var resp types.TaskListResponse
	if err := s.brokerPost("/task/list", listReq, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	data, _ := json.MarshalIndent(resp, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func (s *MCPServer) handleCancelTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	taskID, _ := req.Params.Arguments["task_id"].(string)

	if err := s.brokerPost("/task/cancel", types.TaskCancelRequest{
		TaskID: taskID,
	}, nil); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Best-effort: notify worker about cancellation
	var listResp types.TaskListResponse
	s.brokerPost("/task/list", types.TaskListRequest{TaskIDs: []string{taskID}}, &listResp)
	if len(listResp.Tasks) > 0 && listResp.Tasks[0].ChildID != "" {
		s.brokerPost("/send", types.SendRequest{
			FromID: s.peerID,
			ToID:   listResp.Tasks[0].ChildID,
			Type:   types.TypeAlert,
			Text:   fmt.Sprintf("Task %s has been cancelled. You may stop working on it.", taskID),
		}, nil)
	}

	return mcp.NewToolResultText(`{"ok": true}`), nil
}

// --- HTTP helpers ---

func (s *MCPServer) lookupPeer(peerID string) (*types.Peer, error) {
	var peers []types.Peer
	err := s.brokerPost("/list-peers", types.ListPeersRequest{Scope: "machine"}, &peers)
	if err != nil {
		return nil, err
	}
	for i := range peers {
		if peers[i].ID == peerID {
			return &peers[i], nil
		}
	}
	return nil, fmt.Errorf("peer not found: %s", peerID)
}

func (s *MCPServer) brokerPost(path string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := s.httpClient.Post(s.brokerURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("broker returned %d", resp.StatusCode)
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
