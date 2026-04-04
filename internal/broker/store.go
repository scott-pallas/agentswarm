package broker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/scott-pallas/agentswarm/internal/types"
)

// Store is an in-memory data store replacing SQLite.
type Store struct {
	mu          sync.RWMutex
	peers       map[string]*types.Peer
	messages    []types.Message
	nextMsgID   int64
	context     map[string]*types.ContextEntry // key: "scopeType\x00scopeValue\x00key"
	tasks       map[string]*types.Task
	taskWaiters map[string][]chan struct{}
}

func NewStore() *Store {
	return &Store{
		peers:       make(map[string]*types.Peer),
		nextMsgID:   1,
		context:     make(map[string]*types.ContextEntry),
		tasks:       make(map[string]*types.Task),
		taskWaiters: make(map[string][]chan struct{}),
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
	if !ok {
		return nil, false
	}
	cp := *p
	return &cp, true
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

const maxMessages = 10000

func (s *Store) InsertMessage(m *types.Message) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.ID = s.nextMsgID
	s.nextMsgID++
	s.messages = append(s.messages, *m)
	// Evict oldest messages when limit exceeded
	if len(s.messages) > maxMessages {
		s.messages = s.messages[len(s.messages)-maxMessages:]
	}
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
	if !ok {
		return nil, false
	}
	cp := *e
	return &cp, true
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

// --- Task operations ---

const maxResultSize = 64 * 1024 // 64KB

func (s *Store) CreateTask(parentID, childID, prompt string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := generateStoreID()
	s.tasks[id] = &types.Task{
		TaskID:    id,
		ParentID:  parentID,
		ChildID:   childID,
		Prompt:    prompt,
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return id
}

func (s *Store) GetTask(id string) (*types.Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

func (s *Store) UpdateTask(id, childID, status, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}
	if childID != "" && t.ChildID == "" {
		t.ChildID = childID
	}
	if len(result) > maxResultSize {
		result = result[:maxResultSize]
	}
	t.Status = status
	t.Result = result
	if status == "completed" || status == "failed" || status == "cancelled" {
		t.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.notifyWaiters(id)
	return nil
}

func (s *Store) CancelTask(id string) error {
	return s.UpdateTask(id, "", "cancelled", "")
}

func (s *Store) ListTasks(parentID string, taskIDs []string) []types.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []types.Task
	if len(taskIDs) > 0 {
		for _, id := range taskIDs {
			if t, ok := s.tasks[id]; ok {
				result = append(result, *t)
			}
		}
		return result
	}
	for _, t := range s.tasks {
		if parentID != "" && t.ParentID != parentID {
			continue
		}
		result = append(result, *t)
	}
	return result
}

func (s *Store) FailTasksForPeer(peerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.ChildID == peerID && t.Status == "pending" {
			t.Status = "failed"
			t.Result = "worker process exited unexpectedly"
			t.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			s.notifyWaiters(t.TaskID)
		}
	}
}

func (s *Store) WaitForTasks(taskIDs []string, mode string, timeout time.Duration) []types.TaskResult {
	s.mu.Lock()
	if s.allTerminal(taskIDs, mode) {
		results := s.collectResults(taskIDs)
		s.mu.Unlock()
		return results
	}

	ch := make(chan struct{}, 1)
	for _, id := range taskIDs {
		s.taskWaiters[id] = append(s.taskWaiters[id], ch)
	}
	s.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer s.removeWaiter(taskIDs, ch)
	for {
		select {
		case <-ch:
			s.mu.RLock()
			done := s.allTerminal(taskIDs, mode)
			s.mu.RUnlock()
			if done {
				s.mu.RLock()
				results := s.collectResults(taskIDs)
				s.mu.RUnlock()
				return results
			}
		case <-timer.C:
			s.mu.RLock()
			results := s.collectResults(taskIDs)
			s.mu.RUnlock()
			return results
		}
	}
}

// removeWaiter cleans up waiter channel references to prevent leaks on timeout.
func (s *Store) removeWaiter(taskIDs []string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range taskIDs {
		waiters := s.taskWaiters[id]
		for i, w := range waiters {
			if w == ch {
				s.taskWaiters[id] = append(waiters[:i], waiters[i+1:]...)
				break
			}
		}
		if len(s.taskWaiters[id]) == 0 {
			delete(s.taskWaiters, id)
		}
	}
}

func (s *Store) allTerminal(taskIDs []string, mode string) bool {
	isTerminal := func(status string) bool {
		return status == "completed" || status == "failed" || status == "cancelled"
	}
	for _, id := range taskIDs {
		t, ok := s.tasks[id]
		if !ok {
			continue
		}
		if isTerminal(t.Status) {
			if mode == "any" {
				return true
			}
		} else {
			if mode != "any" {
				return false
			}
		}
	}
	return mode != "any"
}

func (s *Store) collectResults(taskIDs []string) []types.TaskResult {
	var results []types.TaskResult
	for _, id := range taskIDs {
		t, ok := s.tasks[id]
		if !ok {
			continue
		}
		results = append(results, types.TaskResult{
			TaskID:      t.TaskID,
			Status:      t.Status,
			Result:      t.Result,
			PeerID:      t.ChildID,
			CompletedAt: t.CompletedAt,
		})
	}
	return results
}

// notifyWaiters must be called with s.mu held.
func (s *Store) notifyWaiters(taskID string) {
	waiters := s.taskWaiters[taskID]
	for _, ch := range waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	delete(s.taskWaiters, taskID)
}

func generateStoreID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
