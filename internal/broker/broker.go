package broker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/scott-pallas/agentswarm/internal/types"
)

// Broker is the central HTTP server that coordinates peer communication.
type Broker struct {
	db        *DB
	sse       *SSEManager
	startedAt time.Time
}

// New creates a new Broker with the given database.
func New(db *DB) *Broker {
	return &Broker{
		db:        db,
		sse:       NewSSEManager(),
		startedAt: time.Now(),
	}
}

// Handler returns an http.Handler with all routes registered.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()

	// SSE stream
	mux.HandleFunc("GET /stream/{id}", b.handleSSEStream)

	// Health
	mux.HandleFunc("GET /health", b.handleHealth)

	// Peer management
	mux.HandleFunc("POST /register", b.handleRegister)
	mux.HandleFunc("POST /unregister", b.handleUnregister)
	mux.HandleFunc("POST /heartbeat", b.handleHeartbeat)
	mux.HandleFunc("POST /set-summary", b.handleSetSummary)
	mux.HandleFunc("POST /list-peers", b.handleListPeers)

	// Messaging
	mux.HandleFunc("POST /send", b.handleSend)
	mux.HandleFunc("POST /broadcast", b.handleBroadcast)

	// Context store
	mux.HandleFunc("POST /context/set", b.handleContextSet)
	mux.HandleFunc("POST /context/get", b.handleContextGet)
	mux.HandleFunc("POST /context/list", b.handleContextList)

	return mux
}

// StartCleaner runs periodic dead peer cleanup.
func (b *Broker) StartCleaner(interval, staleTimeout time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			stale, err := b.db.CleanStalePeers(staleTimeout)
			if err != nil {
				log.Printf("cleaner error: %v", err)
				continue
			}
			for _, id := range stale {
				b.sse.Push(id, SSEEvent{}) // will fail, just cleanup
				b.sse.Unsubscribe(id)
				// Notify remaining peers
				b.sse.Broadcast(SSEEvent{
					Event: "peer_left",
					Data:  types.PeerLeft{ID: id, Reason: "process_exited"},
				}, "")
				log.Printf("cleaned stale peer: %s", id)
			}
		}
	}()
}

// --- SSE handler ---

func (b *Broker) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	peerID := r.PathValue("id")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := b.sse.Subscribe(peerID)

	// Send connected comment
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Deliver any undelivered messages
	msgs, _ := b.db.UndeliveredMessages(peerID)
	for _, m := range msgs {
		data, _ := json.Marshal(m)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
		b.db.MarkDelivered(m.ID)
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer b.sse.Unsubscribe(peerID)

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(event.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, data)
			flusher.Flush()

		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// --- HTTP handlers ---

func (b *Broker) handleHealth(w http.ResponseWriter, r *http.Request) {
	count, _ := b.db.PeerCount()
	writeJSON(w, types.HealthResponse{
		Status:        "ok",
		Peers:         count,
		UptimeSeconds: int64(time.Since(b.startedAt).Seconds()),
	})
}

func (b *Broker) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req types.RegisterRequest
	if !readJSON(r, w, &req) {
		return
	}

	id := generateID()
	now := time.Now().UTC().Format(time.RFC3339)
	af := req.ActiveFiles
	if af == nil {
		af = []string{}
	}

	peer := &types.Peer{
		ID:           id,
		PID:          req.PID,
		CWD:          req.CWD,
		GitRoot:      req.GitRoot,
		GitBranch:    req.GitBranch,
		TTY:          req.TTY,
		Summary:      req.Summary,
		ActiveFiles:  af,
		RegisteredAt: now,
		LastSeen:     now,
	}

	if err := b.db.InsertPeer(peer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify existing peers
	b.sse.Broadcast(SSEEvent{
		Event: "peer_joined",
		Data:  types.PeerJoined{ID: id, CWD: req.CWD, Summary: req.Summary},
	}, id)

	// Check for file conflicts on registration
	b.detectConflicts(id, af)

	writeJSON(w, types.RegisterResponse{ID: id})
}

func (b *Broker) handleUnregister(w http.ResponseWriter, r *http.Request) {
	var req types.UnregisterRequest
	if !readJSON(r, w, &req) {
		return
	}

	b.db.DeletePeer(req.ID)
	b.sse.Unsubscribe(req.ID)

	b.sse.Broadcast(SSEEvent{
		Event: "peer_left",
		Data:  types.PeerLeft{ID: req.ID, Reason: "unregistered"},
	}, req.ID)

	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req types.HeartbeatRequest
	if !readJSON(r, w, &req) {
		return
	}

	if err := b.db.UpdateHeartbeat(req.ID, req.ActiveFiles, req.GitBranch); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(req.ActiveFiles) > 0 {
		b.detectConflicts(req.ID, req.ActiveFiles)
	}

	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleSetSummary(w http.ResponseWriter, r *http.Request) {
	var req types.SetSummaryRequest
	if !readJSON(r, w, &req) {
		return
	}
	if err := b.db.SetSummary(req.ID, req.Summary); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleListPeers(w http.ResponseWriter, r *http.Request) {
	var req types.ListPeersRequest
	if !readJSON(r, w, &req) {
		return
	}
	peers, err := b.db.ListPeers(req.Scope, req.CWD, req.GitRoot, req.ExcludeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if peers == nil {
		peers = []types.Peer{}
	}
	writeJSON(w, peers)
}

func (b *Broker) handleSend(w http.ResponseWriter, r *http.Request) {
	var req types.SendRequest
	if !readJSON(r, w, &req) {
		return
	}

	if req.Type == "" {
		req.Type = types.TypeMessage
	}
	if req.Urgency == "" {
		req.Urgency = types.UrgencyNormal
	}

	now := time.Now().UTC().Format(time.RFC3339)
	msg := &types.Message{
		Type:     req.Type,
		FromID:   req.FromID,
		ToID:     req.ToID,
		ThreadID: req.ThreadID,
		ReplyTo:  req.ReplyTo,
		Text:     req.Text,
		Urgency:  req.Urgency,
		Context:  req.Context,
		SentAt:   now,
	}

	id, err := b.db.InsertMessage(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg.ID = id

	// Push via SSE
	delivered := b.sse.Push(req.ToID, SSEEvent{Event: "message", Data: msg})
	if delivered {
		b.db.MarkDelivered(id)
	}

	writeJSON(w, types.SendResponse{OK: true, MessageID: id})
}

func (b *Broker) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	var req types.BroadcastRequest
	if !readJSON(r, w, &req) {
		return
	}

	if req.Type == "" {
		req.Type = types.TypeBroadcast
	}
	if req.Urgency == "" {
		req.Urgency = types.UrgencyNormal
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Get peers in scope to determine recipients
	peers, err := b.db.ListPeers(req.Scope, req.CWD, req.GitRoot, req.FromID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	msg := &types.Message{
		Type:    req.Type,
		FromID:  req.FromID,
		Text:    req.Text,
		Urgency: req.Urgency,
		Context: req.Context,
		Scope:   req.Scope,
		SentAt:  now,
	}

	id, err := b.db.InsertMessage(msg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg.ID = id

	var sentTo []string
	for _, p := range peers {
		if b.sse.Push(p.ID, SSEEvent{Event: "broadcast", Data: msg}) {
			sentTo = append(sentTo, p.ID)
		}
	}

	writeJSON(w, types.BroadcastResponse{OK: true, SentTo: sentTo})
}

func (b *Broker) handleContextSet(w http.ResponseWriter, r *http.Request) {
	var req types.ContextSetRequest
	if !readJSON(r, w, &req) {
		return
	}
	scopeType := req.Scope
	if scopeType == "" {
		scopeType = "repo"
	}
	scopeValue := req.ScopeValue
	if scopeValue == "" {
		// Try to get scope value from peer
		if peer, err := b.db.GetPeer(req.PeerID); err == nil {
			switch scopeType {
			case "repo":
				scopeValue = peer.GitRoot
			case "directory":
				scopeValue = peer.CWD
			default:
				scopeValue = "machine"
			}
		}
	}
	if scopeValue == "" {
		scopeValue = "machine"
	}

	if err := b.db.SetContext(req.Key, scopeType, scopeValue, req.Value, req.PeerID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify peers about context update
	b.sse.Broadcast(SSEEvent{
		Event: "context_updated",
		Data: map[string]string{
			"key":        req.Key,
			"set_by":     req.PeerID,
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		},
	}, "")

	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleContextGet(w http.ResponseWriter, r *http.Request) {
	var req types.ContextGetRequest
	if !readJSON(r, w, &req) {
		return
	}
	scopeType := req.Scope
	if scopeType == "" {
		scopeType = "repo"
	}
	scopeValue := req.ScopeValue
	if scopeValue == "" {
		scopeValue = "machine"
	}

	entry, err := b.db.GetContext(req.Key, scopeType, scopeValue)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeJSON(w, types.ContextGetResponse{
		Value:     entry.Value,
		SetBy:     entry.SetBy,
		UpdatedAt: entry.UpdatedAt,
	})
}

func (b *Broker) handleContextList(w http.ResponseWriter, r *http.Request) {
	var req types.ContextListRequest
	if !readJSON(r, w, &req) {
		return
	}

	entries, err := b.db.ListContext(req.Scope, req.ScopeValue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []types.ContextEntry{}
	}
	writeJSON(w, types.ContextListResponse{Entries: entries})
}

// --- conflict detection ---

func (b *Broker) detectConflicts(peerID string, activeFiles []string) {
	if len(activeFiles) == 0 {
		return
	}
	peers, err := b.db.AllPeers()
	if err != nil {
		return
	}
	for _, other := range peers {
		if other.ID == peerID {
			continue
		}
		var overlap []string
		otherSet := make(map[string]bool)
		for _, f := range other.ActiveFiles {
			otherSet[f] = true
		}
		for _, f := range activeFiles {
			if otherSet[f] {
				overlap = append(overlap, f)
			}
		}
		if len(overlap) > 0 {
			alert := types.ConflictAlert{
				Files:      overlap,
				Peers:      []string{peerID, other.ID},
				DetectedAt: time.Now().UTC().Format(time.RFC3339),
			}
			b.sse.Push(peerID, SSEEvent{Event: "conflict", Data: alert})
			b.sse.Push(other.ID, SSEEvent{Event: "conflict", Data: alert})
		}
	}
}

// --- helpers ---

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func readJSON(r *http.Request, w http.ResponseWriter, v interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
