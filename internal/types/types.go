// Package types defines all shared data structures for agentswarm.
package types

import "encoding/json"

// MessageType enumerates the kinds of messages peers can exchange.
type MessageType string

const (
	TypeMessage      MessageType = "message"
	TypeQuestion     MessageType = "question"
	TypeResponse     MessageType = "response"
	TypeAlert        MessageType = "alert"
	TypeNotification MessageType = "notification"
	TypeRequest      MessageType = "request"
	TypeBroadcast    MessageType = "broadcast"
)

// Peer represents a registered Claude Code session.
type Peer struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	PID          int      `json:"pid"`
	CWD          string   `json:"cwd"`
	GitRoot      string   `json:"git_root,omitempty"`
	GitBranch    string   `json:"git_branch,omitempty"`
	TTY          string   `json:"tty,omitempty"`
	Summary      string   `json:"summary"`
	ActiveFiles  []string `json:"active_files"`
	RegisteredAt string   `json:"registered_at"`
	LastSeen     string   `json:"last_seen"`
}

// Message represents a peer-to-peer or broadcast message.
type Message struct {
	ID        int64           `json:"id"`
	Type      MessageType     `json:"type"`
	FromID    string          `json:"from_id"`
	ToID      string          `json:"to_id,omitempty"`
	Text      string          `json:"text"`
	Context   json.RawMessage `json:"context,omitempty"`
	Scope     string          `json:"scope,omitempty"`
	SentAt    string          `json:"sent_at"`
	Delivered bool            `json:"delivered"`
}

// MessageContext is the optional structured context attached to a message.
type MessageContext struct {
	Files    []string               `json:"files,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ContextEntry represents a shared key-value context item.
type ContextEntry struct {
	Key        string `json:"key"`
	ScopeType  string `json:"scope_type"`
	ScopeValue string `json:"scope_value"`
	Value      string `json:"value"`
	SetBy      string `json:"set_by"`
	UpdatedAt  string `json:"updated_at"`
}

// PeerJoined is the SSE event when a new peer registers.
type PeerJoined struct {
	ID      string `json:"id"`
	CWD     string `json:"cwd"`
	Summary string `json:"summary"`
}

// PeerLeft is the SSE event when a peer unregisters or is cleaned up.
type PeerLeft struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status        string `json:"status"`
	Service       string `json:"service"`
	Peers         int    `json:"peers"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// --- Request/Response types for broker HTTP API ---

type RegisterRequest struct {
	PID         int      `json:"pid"`
	Name        string   `json:"name,omitempty"`
	CWD         string   `json:"cwd"`
	GitRoot     string   `json:"git_root,omitempty"`
	GitBranch   string   `json:"git_branch,omitempty"`
	TTY         string   `json:"tty,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	ActiveFiles []string `json:"active_files,omitempty"`
}

type RegisterResponse struct {
	ID string `json:"id"`
}

type UnregisterRequest struct {
	ID string `json:"id"`
}

type HeartbeatRequest struct {
	ID          string   `json:"id"`
	ActiveFiles []string `json:"active_files,omitempty"`
	GitBranch   string   `json:"git_branch,omitempty"`
}

type SetSummaryRequest struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type SetNameRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ListPeersRequest struct {
	Scope     string `json:"scope"`
	CWD       string `json:"cwd,omitempty"`
	GitRoot   string `json:"git_root,omitempty"`
	ExcludeID string `json:"exclude_id,omitempty"`
}

type SendRequest struct {
	FromID  string          `json:"from_id"`
	ToID    string          `json:"to_id"`
	Type    MessageType     `json:"type,omitempty"`
	Text    string          `json:"text"`
	Context json.RawMessage `json:"context,omitempty"`
}

type SendResponse struct {
	OK        bool  `json:"ok"`
	MessageID int64 `json:"message_id"`
}

type BroadcastRequest struct {
	FromID  string          `json:"from_id"`
	Scope   string          `json:"scope"`
	Type    MessageType     `json:"type,omitempty"`
	Text    string          `json:"text"`
	Context json.RawMessage `json:"context,omitempty"`
	CWD     string          `json:"cwd,omitempty"`
	GitRoot string          `json:"git_root,omitempty"`
}

type BroadcastResponse struct {
	OK     bool     `json:"ok"`
	SentTo []string `json:"sent_to"`
}

type ContextSetRequest struct {
	PeerID     string `json:"peer_id"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Scope      string `json:"scope,omitempty"`
	ScopeValue string `json:"scope_value,omitempty"`
}

type ContextGetRequest struct {
	Key        string `json:"key"`
	Scope      string `json:"scope,omitempty"`
	ScopeValue string `json:"scope_value,omitempty"`
}

type ContextGetResponse struct {
	Value     string `json:"value"`
	SetBy     string `json:"set_by"`
	UpdatedAt string `json:"updated_at"`
}

type ContextListRequest struct {
	Scope      string `json:"scope,omitempty"`
	ScopeValue string `json:"scope_value,omitempty"`
}

type ContextListResponse struct {
	Entries []ContextEntry `json:"entries"`
}

type OKResponse struct {
	OK bool `json:"ok"`
}
