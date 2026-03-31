package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/scott-pallas/agentswarm/internal/types"
)

// MCPServer wraps the MCP stdio server and broker communication.
type MCPServer struct {
	brokerURL string
	peerID    string
	peerCtx   *PeerContext
	mcpSrv    *mcpserver.MCPServer
	sseClient *SSEClient
}

// NewMCPServer creates and configures the MCP server with all tools.
func NewMCPServer(brokerURL string) *MCPServer {
	s := &MCPServer{
		brokerURL: brokerURL,
	}

	s.mcpSrv = mcpserver.NewMCPServer(
		"agentswarm",
		"0.1.0",
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
   - "request" when delegating a task to another peer`),
	)

	s.registerTools()
	return s
}

func (s *MCPServer) registerTools() {
	// list_peers
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

	// send_message
	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "send_message",
		Description: "Send a message to another Claude Code instance. Arrives instantly.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"to_id":     map[string]interface{}{"type": "string", "description": "Target peer ID"},
				"text":      map[string]interface{}{"type": "string", "description": "Message content"},
				"type":      map[string]interface{}{"type": "string", "enum": []string{"message", "question", "request", "alert", "notification"}, "description": "Message type"},
				"urgency":   map[string]interface{}{"type": "string", "enum": []string{"low", "normal", "high"}, "description": "Message urgency"},
				"thread_id": map[string]interface{}{"type": "string", "description": "Thread ID for ongoing conversations (optional)"},
				"files":     map[string]interface{}{"type": "array", "description": "Relevant file paths for context (optional)"},
			},
			Required: []string{"to_id", "text"},
		},
	}, s.handleSendMessage)

	// broadcast
	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "broadcast",
		Description: "Send a message to all peers in a scope (repo, directory, or machine).",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"text":    map[string]interface{}{"type": "string", "description": "Message content"},
				"scope":   map[string]interface{}{"type": "string", "enum": []string{"machine", "directory", "repo"}, "description": "Broadcast scope"},
				"type":    map[string]interface{}{"type": "string", "enum": []string{"message", "alert", "notification"}, "description": "Message type"},
				"urgency": map[string]interface{}{"type": "string", "enum": []string{"low", "normal", "high"}, "description": "Message urgency"},
			},
			Required: []string{"text", "scope"},
		},
	}, s.handleBroadcast)

	// set_summary
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

	// get_context
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

	// set_context
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

	// check_messages
	s.mcpSrv.AddTool(mcp.Tool{
		Name:        "check_messages",
		Description: "Manually check for messages. Normally messages arrive automatically via push — use this as a fallback.",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, s.handleCheckMessages)
}

// Start registers with the broker, opens SSE, and runs the MCP stdio server.
func (s *MCPServer) Start(ctx context.Context) error {
	s.peerCtx = DetectContext()

	// Register with broker
	if err := s.register(); err != nil {
		return fmt.Errorf("register with broker: %w", err)
	}
	log.Printf("registered as peer %s", s.peerID)

	// Start SSE client
	s.sseClient = NewSSEClient(s.brokerURL, s.peerID, s.handleSSEEvent)
	s.sseClient.Start()

	// Start heartbeat
	go s.heartbeatLoop(ctx)

	// Run MCP stdio server
	stdio := mcpserver.NewStdioServer(s.mcpSrv)
	return stdio.Listen(ctx, os.Stdin, os.Stdout)
}

// Shutdown cleans up broker registration and SSE.
func (s *MCPServer) Shutdown() {
	if s.sseClient != nil {
		s.sseClient.Stop()
	}
	if s.peerID != "" {
		s.brokerPost("/unregister", types.UnregisterRequest{ID: s.peerID}, nil)
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
			s.brokerPost("/heartbeat", types.HeartbeatRequest{
				ID:          s.peerID,
				ActiveFiles: s.peerCtx.ActiveFiles,
				GitBranch:   s.peerCtx.GitBranch,
			}, nil)
		}
	}
}

func (s *MCPServer) handleSSEEvent(event, data string) {
	// Push as channel notification to Claude Code
	var content string
	switch event {
	case "message", "broadcast":
		var msg types.Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			return
		}
		content = fmt.Sprintf("[%s from %s] %s", msg.Type, msg.FromID, msg.Text)
	case "conflict":
		var alert types.ConflictAlert
		if err := json.Unmarshal([]byte(data), &alert); err != nil {
			return
		}
		content = fmt.Sprintf("⚠️ FILE CONFLICT: peers %v are editing the same files: %v", alert.Peers, alert.Files)
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

	// Send as channel notification via MCP
	s.mcpSrv.SendNotificationToAllClients("notifications/claude/channel", map[string]interface{}{
		"channel": "agentswarm",
		"body":    content,
	})
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
	urgency, _ := args["urgency"].(string)
	threadID, _ := args["thread_id"].(string)

	if msgType == "" {
		msgType = "message"
	}
	if urgency == "" {
		urgency = "normal"
	}

	brokerReq := types.SendRequest{
		FromID:   s.peerID,
		ToID:     toID,
		Type:     types.MessageType(msgType),
		Text:     text,
		Urgency:  types.Urgency(urgency),
		ThreadID: threadID,
	}

	// Handle files context
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
	urgency, _ := args["urgency"].(string)

	if msgType == "" {
		msgType = "notification"
	}
	if urgency == "" {
		urgency = "normal"
	}

	brokerReq := types.BroadcastRequest{
		FromID:  s.peerID,
		Scope:   scope,
		Type:    types.MessageType(msgType),
		Text:    text,
		Urgency: types.Urgency(urgency),
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

func (s *MCPServer) handleCheckMessages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Messages are normally pushed via SSE — this is a manual fallback
	return mcp.NewToolResultText("Messages are delivered automatically via SSE push. If you haven't received any, there are no pending messages."), nil
}

// --- HTTP helpers ---

func (s *MCPServer) brokerPost(path string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := http.Post(s.brokerURL+path, "application/json", bytes.NewReader(data))
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

// EnsureBroker checks if the broker is running and starts it if not.
func EnsureBroker(brokerURL string) error {
	resp, err := http.Get(brokerURL + "/health")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return nil // already running
		}
	}

	log.Println("broker not running, starting...")
	cmd := exec.Command("agentswarm-broker")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start broker: %w", err)
	}

	// Wait for broker to be ready
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		resp, err := http.Get(brokerURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				log.Println("broker started")
				return nil
			}
		}
	}
	return fmt.Errorf("broker did not start in time")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
