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
	store     *Store
	sse       *SSEManager
	startedAt time.Time
}

// New creates a new Broker with in-memory storage.
func New() *Broker {
	return &Broker{
		store:     NewStore(),
		sse:       NewSSEManager(),
		startedAt: time.Now(),
	}
}

// Handler returns an http.Handler with all routes registered.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /stream/{id}", b.handleSSEStream)
	mux.HandleFunc("GET /health", b.handleHealth)

	mux.HandleFunc("POST /register", b.handleRegister)
	mux.HandleFunc("POST /unregister", b.handleUnregister)
	mux.HandleFunc("POST /heartbeat", b.handleHeartbeat)
	mux.HandleFunc("POST /set-summary", b.handleSetSummary)
	mux.HandleFunc("POST /set-name", b.handleSetName)
	mux.HandleFunc("POST /list-peers", b.handleListPeers)

	mux.HandleFunc("POST /send", b.handleSend)
	mux.HandleFunc("POST /broadcast", b.handleBroadcast)

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
			stale := b.store.CleanStalePeers(staleTimeout)
			for _, id := range stale {
				b.sse.Unsubscribe(id)
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

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Deliver any undelivered messages
	for _, m := range b.store.UndeliveredMessages(peerID) {
		data, _ := json.Marshal(m)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
		b.store.MarkDelivered(m.ID)
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
	writeJSON(w, types.HealthResponse{
		Status:        "ok",
		Service:       "agentswarm",
		Peers:         b.store.PeerCount(),
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
		Name:         req.Name,
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

	b.store.InsertPeer(peer)

	b.sse.Broadcast(SSEEvent{
		Event: "peer_joined",
		Data:  types.PeerJoined{ID: id, CWD: req.CWD, Summary: req.Summary},
	}, id)

	writeJSON(w, types.RegisterResponse{ID: id})
}

func (b *Broker) handleUnregister(w http.ResponseWriter, r *http.Request) {
	var req types.UnregisterRequest
	if !readJSON(r, w, &req) {
		return
	}

	b.store.DeletePeer(req.ID)
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
	b.store.UpdateHeartbeat(req.ID, req.ActiveFiles, req.GitBranch)
	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleSetSummary(w http.ResponseWriter, r *http.Request) {
	var req types.SetSummaryRequest
	if !readJSON(r, w, &req) {
		return
	}
	b.store.SetSummary(req.ID, req.Summary)
	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleSetName(w http.ResponseWriter, r *http.Request) {
	var req types.SetNameRequest
	if !readJSON(r, w, &req) {
		return
	}
	b.store.SetName(req.ID, req.Name)
	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleListPeers(w http.ResponseWriter, r *http.Request) {
	var req types.ListPeersRequest
	if !readJSON(r, w, &req) {
		return
	}
	peers := b.store.ListPeers(req.Scope, req.CWD, req.GitRoot, req.ExcludeID)
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

	now := time.Now().UTC().Format(time.RFC3339)
	msg := &types.Message{
		Type:    req.Type,
		FromID:  req.FromID,
		ToID:    req.ToID,
		Text:    req.Text,
		Context: req.Context,
		SentAt:  now,
	}

	id := b.store.InsertMessage(msg)

	delivered := b.sse.Push(req.ToID, SSEEvent{Event: "message", Data: msg})
	if delivered {
		b.store.MarkDelivered(id)
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

	now := time.Now().UTC().Format(time.RFC3339)
	peers := b.store.ListPeers(req.Scope, req.CWD, req.GitRoot, req.FromID)

	msg := &types.Message{
		Type:    req.Type,
		FromID:  req.FromID,
		Text:    req.Text,
		Context: req.Context,
		Scope:   req.Scope,
		SentAt:  now,
	}

	b.store.InsertMessage(msg)

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
		if peer, ok := b.store.GetPeer(req.PeerID); ok {
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

	b.store.SetContext(req.Key, scopeType, scopeValue, req.Value, req.PeerID)

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

	entry, ok := b.store.GetContext(req.Key, scopeType, scopeValue)
	if !ok {
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
	entries := b.store.ListContext(req.Scope, req.ScopeValue)
	if entries == nil {
		entries = []types.ContextEntry{}
	}
	writeJSON(w, types.ContextListResponse{Entries: entries})
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
