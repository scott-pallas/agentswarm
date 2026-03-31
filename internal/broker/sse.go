package broker

import (
	"sync"
)

// SSEEvent is a typed event pushed over an SSE connection.
type SSEEvent struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data"`
}

// SSEManager tracks per-peer SSE channels and handles push/broadcast.
type SSEManager struct {
	mu    sync.RWMutex
	conns map[string]chan SSEEvent
}

// NewSSEManager creates a new SSE connection manager.
func NewSSEManager() *SSEManager {
	return &SSEManager{conns: make(map[string]chan SSEEvent)}
}

// Subscribe registers a peer's SSE channel. Returns the receive channel.
func (m *SSEManager) Subscribe(peerID string) chan SSEEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Close existing connection if any
	if old, ok := m.conns[peerID]; ok {
		close(old)
	}
	ch := make(chan SSEEvent, 64)
	m.conns[peerID] = ch
	return ch
}

// Unsubscribe removes a peer's SSE channel.
func (m *SSEManager) Unsubscribe(peerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.conns[peerID]; ok {
		close(ch)
		delete(m.conns, peerID)
	}
}

// Push sends an event to a specific peer. Returns false if peer not connected.
func (m *SSEManager) Push(peerID string, event SSEEvent) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.conns[peerID]
	if !ok {
		return false
	}
	select {
	case ch <- event:
		return true
	default:
		return false // channel full
	}
}

// Broadcast sends an event to all connected peers except the excluded one.
func (m *SSEManager) Broadcast(event SSEEvent, exclude string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var sent []string
	for id, ch := range m.conns {
		if id == exclude {
			continue
		}
		select {
		case ch <- event:
			sent = append(sent, id)
		default:
			// skip slow peers
		}
	}
	return sent
}

// IsConnected checks if a peer has an active SSE connection.
func (m *SSEManager) IsConnected(peerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.conns[peerID]
	return ok
}
