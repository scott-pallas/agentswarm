package broker

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/scott-pallas/agentswarm/internal/types"
)

// Store is an in-memory data store replacing SQLite.
type Store struct {
	mu        sync.RWMutex
	peers     map[string]*types.Peer
	messages  []types.Message
	nextMsgID int64
	context   map[string]*types.ContextEntry // key: "scopeType\x00scopeValue\x00key"
}

func NewStore() *Store {
	return &Store{
		peers:     make(map[string]*types.Peer),
		nextMsgID: 1,
		context:   make(map[string]*types.ContextEntry),
	}
}

func (s *Store) InsertPeer(p *types.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[p.ID] = p
}

func (s *Store) DeletePeer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, id)
}

func (s *Store) GetPeer(id string) (*types.Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[id]
	return p, ok
}

func (s *Store) SetName(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[id]; ok {
		p.Name = name
	}
}

func (s *Store) SetSummary(id, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[id]; ok {
		p.Summary = summary
	}
}

func (s *Store) UpdateHeartbeat(id string, activeFiles []string, gitBranch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[id]
	if !ok {
		return
	}
	p.LastSeen = time.Now().UTC().Format(time.RFC3339)
	if len(activeFiles) > 0 {
		p.ActiveFiles = activeFiles
	}
	if gitBranch != "" {
		p.GitBranch = gitBranch
	}
}

func (s *Store) ListPeers(scope, cwd, gitRoot, excludeID string) []types.Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []types.Peer
	for _, p := range s.peers {
		if p.ID == excludeID {
			continue
		}
		switch scope {
		case "repo":
			if p.GitRoot != gitRoot {
				continue
			}
		case "directory":
			if p.CWD != cwd {
				continue
			}
		}
		result = append(result, *p)
	}
	return result
}

func (s *Store) AllPeers() []types.Peer {
	return s.ListPeers("machine", "", "", "")
}

func (s *Store) PeerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers)
}

func (s *Store) CleanStalePeers(timeout time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().Add(-timeout)
	var stale []string
	for id, p := range s.peers {
		lastSeen, err := time.Parse(time.RFC3339, p.LastSeen)
		if err != nil || lastSeen.Before(cutoff) {
			proc, err := os.FindProcess(p.PID)
			if err != nil || !isProcessAlive(proc) {
				stale = append(stale, id)
			}
		}
	}
	for _, id := range stale {
		delete(s.peers, id)
	}
	return stale
}

func (s *Store) InsertMessage(m *types.Message) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.ID = s.nextMsgID
	s.nextMsgID++
	s.messages = append(s.messages, *m)
	return m.ID
}

func (s *Store) UndeliveredMessages(peerID string) []types.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []types.Message
	for _, m := range s.messages {
		if m.ToID == peerID && !m.Delivered {
			result = append(result, m)
		}
	}
	return result
}

func (s *Store) MarkDelivered(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.messages {
		if s.messages[i].ID == id {
			s.messages[i].Delivered = true
			return
		}
	}
}

func contextKey(scopeType, scopeValue, key string) string {
	return fmt.Sprintf("%s\x00%s\x00%s", scopeType, scopeValue, key)
}

func (s *Store) SetContext(key, scopeType, scopeValue, value, setBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ck := contextKey(scopeType, scopeValue, key)
	s.context[ck] = &types.ContextEntry{
		Key:        key,
		ScopeType:  scopeType,
		ScopeValue: scopeValue,
		Value:      value,
		SetBy:      setBy,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}

func (s *Store) GetContext(key, scopeType, scopeValue string) (*types.ContextEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.context[contextKey(scopeType, scopeValue, key)]
	return e, ok
}

func (s *Store) ListContext(scopeType, scopeValue string) []types.ContextEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []types.ContextEntry
	for _, e := range s.context {
		if scopeType != "" && scopeValue != "" {
			if e.ScopeType != scopeType || e.ScopeValue != scopeValue {
				continue
			}
		}
		result = append(result, *e)
	}
	return result
}

func isProcessAlive(proc *os.Process) bool {
	err := proc.Signal(os.Signal(nil))
	return err == nil
}
