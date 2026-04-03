# Orchestration Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add orchestration tools (delegate, wait_for_result, etc.) to agentswarm so agents can compose into workflows, plus rewrite stale docs and add release infrastructure.

**Architecture:** 7 new MCP tools backed by a task tracking system in the broker's in-memory store. Long-poll endpoint for blocking waits. Channel-based message buffering for wait_for_messages. No new dependencies.

**Tech Stack:** Go 1.24, mcp-go SDK, GitHub Actions for CI/release

---

### Task 1: Types — Add Task and orchestration request/response types

**Files:**
- Modify: `internal/types/types.go`
- Test: `go build ./...` (type-check)

- [ ] **Step 1: Add Task and TaskResult types**

Add after the ContextEntry type:

```go
// Task represents a delegated task tracked by the broker.
type Task struct {
	TaskID      string `json:"task_id"`
	ParentID    string `json:"parent_id"`
	ChildID     string `json:"child_id,omitempty"`
	Prompt      string `json:"prompt"`
	Status      string `json:"status"` // pending, completed, failed, cancelled
	Result      string `json:"result,omitempty"`
	CreatedAt   string `json:"created_at"`
	CompletedAt string `json:"completed_at,omitempty"`
}

// TaskResult is returned by wait_for_result.
type TaskResult struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	Result      string `json:"result,omitempty"`
	PeerID      string `json:"peer_id,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}
```

- [ ] **Step 2: Add request/response types for broker endpoints**

```go
type TaskCreateRequest struct {
	ParentID string `json:"parent_id"`
	ChildID  string `json:"child_id,omitempty"`
	Prompt   string `json:"prompt"`
}

type TaskCreateResponse struct {
	TaskID string `json:"task_id"`
}

type TaskUpdateRequest struct {
	TaskID  string `json:"task_id"`
	ChildID string `json:"child_id,omitempty"`
	Status  string `json:"status"`
	Result  string `json:"result,omitempty"`
}

type TaskWaitRequest struct {
	TaskIDs        []string `json:"task_ids"`
	Mode           string   `json:"mode,omitempty"` // "any" or "all", default "all"
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type TaskWaitResponse struct {
	Results  []TaskResult `json:"results"`
	TimedOut bool         `json:"timed_out"`
}

type TaskListRequest struct {
	ParentID string   `json:"parent_id,omitempty"`
	TaskIDs  []string `json:"task_ids,omitempty"`
}

type TaskListResponse struct {
	Tasks []Task `json:"tasks"`
}

type TaskCancelRequest struct {
	TaskID string `json:"task_id"`
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: clean build

- [ ] **Step 4: Commit**

```bash
git add internal/types/types.go
git commit -m "feat: add Task and orchestration request/response types"
```

---

### Task 2: Store — Add task storage and waiter channels

**Files:**
- Modify: `internal/broker/store.go`
- Create: `internal/broker/store_test.go`

- [ ] **Step 1: Write tests for task store operations**

```go
package broker

import (
	"testing"
	"time"

	"github.com/scott-pallas/agentswarm/internal/types"
)

func TestCreateAndGetTask(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "", "do something")
	task, ok := s.GetTask(id)
	if !ok {
		t.Fatal("task not found")
	}
	if task.ParentID != "parent1" || task.Status != "pending" || task.Prompt != "do something" {
		t.Errorf("unexpected task: %+v", task)
	}
}

func TestUpdateTask(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "", "do something")
	err := s.UpdateTask(id, "child1", "completed", "done!")
	if err != nil {
		t.Fatal(err)
	}
	task, _ := s.GetTask(id)
	if task.Status != "completed" || task.Result != "done!" || task.ChildID != "child1" {
		t.Errorf("unexpected task after update: %+v", task)
	}
	if task.CompletedAt == "" {
		t.Error("completed_at should be set")
	}
}

func TestUpdateTaskSetsChildIDIfEmpty(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "", "do something")
	s.UpdateTask(id, "child1", "completed", "done!")
	task, _ := s.GetTask(id)
	if task.ChildID != "child1" {
		t.Errorf("child_id should be set: got %q", task.ChildID)
	}
}

func TestUpdateTaskDoesNotOverwriteChildID(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "original_child", "do something")
	s.UpdateTask(id, "new_child", "completed", "done!")
	task, _ := s.GetTask(id)
	if task.ChildID != "original_child" {
		t.Errorf("child_id should not be overwritten: got %q", task.ChildID)
	}
}

func TestCancelTask(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "child1", "do something")
	err := s.CancelTask(id)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := s.GetTask(id)
	if task.Status != "cancelled" {
		t.Errorf("expected cancelled, got %s", task.Status)
	}
}

func TestListTasksByParent(t *testing.T) {
	s := NewStore()
	s.CreateTask("parent1", "", "task A")
	s.CreateTask("parent1", "", "task B")
	s.CreateTask("parent2", "", "task C")
	tasks := s.ListTasks("parent1", nil)
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestListTasksByIDs(t *testing.T) {
	s := NewStore()
	id1 := s.CreateTask("parent1", "", "task A")
	s.CreateTask("parent1", "", "task B")
	tasks := s.ListTasks("", []string{id1})
	if len(tasks) != 1 || tasks[0].TaskID != id1 {
		t.Errorf("expected 1 task with id %s, got %+v", id1, tasks)
	}
}

func TestWaitForTask(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "", "do something")

	done := make(chan []types.TaskResult)
	go func() {
		results := s.WaitForTasks([]string{id}, "all", 5*time.Second)
		done <- results
	}()

	time.Sleep(50 * time.Millisecond)
	s.UpdateTask(id, "child1", "completed", "result!")

	results := <-done
	if len(results) != 1 || results[0].Status != "completed" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestWaitForTaskTimeout(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "", "do something")
	results := s.WaitForTasks([]string{id}, "all", 100*time.Millisecond)
	if len(results) != 1 || results[0].Status != "pending" {
		t.Errorf("expected pending on timeout: %+v", results)
	}
}

func TestFailTasksForPeer(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "dead_peer", "do something")
	s.FailTasksForPeer("dead_peer")
	task, _ := s.GetTask(id)
	if task.Status != "failed" {
		t.Errorf("expected failed, got %s", task.Status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/broker/ -v -run TestCreate`
Expected: compilation error — methods don't exist yet

- [ ] **Step 3: Implement task storage methods**

Add to `internal/broker/store.go`:

```go
// Add to Store struct fields:
// tasks     map[string]*types.Task
// taskWaiters map[string][]chan struct{}

// Add to NewStore():
// tasks:       make(map[string]*types.Task),
// taskWaiters: make(map[string][]chan struct{}),
```

Then add methods:

```go
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
		if t.ChildID == peerID && (t.Status == "pending") {
			t.Status = "failed"
			t.Result = "worker process exited unexpectedly"
			t.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			s.notifyWaiters(t.TaskID)
		}
	}
}

func (s *Store) WaitForTasks(taskIDs []string, mode string, timeout time.Duration) []types.TaskResult {
	// Check if already terminal
	s.mu.Lock()
	if s.allTerminal(taskIDs, mode) {
		results := s.collectResults(taskIDs)
		s.mu.Unlock()
		return results
	}

	// Register waiters
	ch := make(chan struct{}, 1)
	for _, id := range taskIDs {
		s.taskWaiters[id] = append(s.taskWaiters[id], ch)
	}
	s.mu.Unlock()

	// Wait for notification or timeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
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
	rand.Read(b)
	return hex.EncodeToString(b)
}
```

Add `"crypto/rand"` and `"encoding/hex"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/broker/ -v`
Expected: all tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/broker/store.go internal/broker/store_test.go
git commit -m "feat: add task storage with wait/cancel/failure detection"
```

---

### Task 3: Broker — Add task HTTP endpoints

**Files:**
- Modify: `internal/broker/broker.go`

- [ ] **Step 1: Register new routes in Handler()**

Add after the context routes:

```go
mux.HandleFunc("POST /task/create", b.handleTaskCreate)
mux.HandleFunc("POST /task/update", b.handleTaskUpdate)
mux.HandleFunc("POST /task/wait", b.handleTaskWait)
mux.HandleFunc("POST /task/list", b.handleTaskList)
mux.HandleFunc("POST /task/cancel", b.handleTaskCancel)
```

- [ ] **Step 2: Implement handler functions**

```go
func (b *Broker) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	var req types.TaskCreateRequest
	if !readJSON(r, w, &req) {
		return
	}
	taskID := b.store.CreateTask(req.ParentID, req.ChildID, req.Prompt)
	writeJSON(w, types.TaskCreateResponse{TaskID: taskID})
}

func (b *Broker) handleTaskUpdate(w http.ResponseWriter, r *http.Request) {
	var req types.TaskUpdateRequest
	if !readJSON(r, w, &req) {
		return
	}
	if err := b.store.UpdateTask(req.TaskID, req.ChildID, req.Status, req.Result); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, types.OKResponse{OK: true})
}

func (b *Broker) handleTaskWait(w http.ResponseWriter, r *http.Request) {
	var req types.TaskWaitRequest
	if !readJSON(r, w, &req) {
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "all"
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}

	results := b.store.WaitForTasks(req.TaskIDs, mode, timeout)

	// Determine if we timed out by checking if any non-terminal tasks remain
	timedOut := false
	for _, r := range results {
		if r.Status == "pending" {
			timedOut = true
			break
		}
	}

	writeJSON(w, types.TaskWaitResponse{Results: results, TimedOut: timedOut})
}

func (b *Broker) handleTaskList(w http.ResponseWriter, r *http.Request) {
	var req types.TaskListRequest
	if !readJSON(r, w, &req) {
		return
	}
	tasks := b.store.ListTasks(req.ParentID, req.TaskIDs)
	if tasks == nil {
		tasks = []types.Task{}
	}
	writeJSON(w, types.TaskListResponse{Tasks: tasks})
}

func (b *Broker) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	var req types.TaskCancelRequest
	if !readJSON(r, w, &req) {
		return
	}
	if err := b.store.CancelTask(req.TaskID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, types.OKResponse{OK: true})
}
```

- [ ] **Step 3: Extend cleaner for task failure detection**

In `StartCleaner`, after `b.sse.Broadcast(...)` inside the stale peer loop, add:

```go
b.store.FailTasksForPeer(id)
```

- [ ] **Step 4: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/broker/broker.go
git commit -m "feat: add task broker endpoints and cleaner integration"
```

---

### Task 4: MCP Server — Add message channel for wait_for_messages

**Files:**
- Modify: `internal/server/mcp.go`

- [ ] **Step 1: Add message channel to MCPServer struct**

Add field to MCPServer:

```go
msgChan chan string // buffered channel for incoming messages from SSE
```

Initialize in NewMCPServer:

```go
s.msgChan = make(chan string, 64)
```

- [ ] **Step 2: Feed messages into the channel from SSE handler**

In `handleSSEEvent`, at the end of the function (before `SendNotificationToAllClients`), add:

```go
// Feed messages into the channel for wait_for_messages
select {
case s.msgChan <- content:
default:
	// channel full, drop (messages still delivered via SSE push)
}
```

- [ ] **Step 3: Implement wait_for_messages handler**

Replace `handleCheckMessages` with:

```go
func (s *MCPServer) handleWaitForMessages(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	timeoutSec := 120.0
	if t, ok := req.Params.Arguments["timeout_seconds"].(float64); ok {
		timeoutSec = t
	}

	if timeoutSec == 0 {
		// Non-blocking: drain whatever is in the channel
		var messages []string
		for {
			select {
			case msg := <-s.msgChan:
				messages = append(messages, msg)
			default:
				goto done
			}
		}
	done:
		result := map[string]interface{}{
			"messages":  messages,
			"timed_out": false,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	// Blocking: wait for messages up to timeout
	timeout := time.Duration(timeoutSec * float64(time.Second))
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var messages []string
	// Wait for at least one message
	select {
	case msg := <-s.msgChan:
		messages = append(messages, msg)
	case <-timer.C:
		result := map[string]interface{}{
			"messages":  []string{},
			"timed_out": true,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}

	// Drain any additional messages that arrived
	for {
		select {
		case msg := <-s.msgChan:
			messages = append(messages, msg)
		default:
			goto finish
		}
	}
finish:
	result := map[string]interface{}{
		"messages":  messages,
		"timed_out": false,
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}
```

- [ ] **Step 4: Update tool registration**

Replace the `check_messages` tool registration with `wait_for_messages`:

```go
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
```

Keep `check_messages` as alias:

```go
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
```

- [ ] **Step 5: Build and test**

Run: `go build ./... && go test ./...`
Expected: clean build, all tests pass

- [ ] **Step 6: Commit**

```bash
git add internal/server/mcp.go
git commit -m "feat: add wait_for_messages tool with check_messages alias"
```

---

### Task 5: MCP Server — Add orchestration tools (delegate, request_task, report_result, wait_for_result, list_tasks, cancel_task)

**Files:**
- Modify: `internal/server/mcp.go`
- Modify: `internal/server/spawn.go`

- [ ] **Step 1: Update buildSpawnPrompt for delegated tasks**

Add `taskID` parameter to `buildSpawnPrompt` in spawn.go:

```go
func buildSpawnPrompt(userPrompt, parentPeerID, name string, interactive bool, taskID string) string {
	var prompt string
	if name != "" {
		prompt = fmt.Sprintf("On your first turn, call set_name(%q) to identify yourself in the swarm.\n\n", name)
	}
	prompt += fmt.Sprintf("You were spawned by agentswarm peer %s.", parentPeerID)
	if taskID != "" {
		prompt += fmt.Sprintf(" Your task ID is %s.", taskID)
		prompt += "\n\nWhen you finish, call report_result(\"" + taskID + "\", \"your result summary\")."
		prompt += "\nIf you encounter an error you cannot resolve, call report_result(\"" + taskID + "\", \"description of failure\", \"failed\")."
	} else if interactive {
		prompt += " You are in INTERACTIVE mode — you must stay alive and respond to all incoming messages. Do NOT exit or stop. When you receive a channel message, respond immediately using send_message."
	} else {
		prompt += " When you finish your task, send your results back to that peer using send_message."
	}
	prompt += fmt.Sprintf("\n\nYour task:\n%s", userPrompt)
	return prompt
}
```

- [ ] **Step 2: Update all callsites for buildSpawnPrompt**

In mcp.go `handleSpawnAgent`, update the call:

```go
augmented := buildSpawnPrompt(prompt, s.peerID, name, interactive, "")
```

- [ ] **Step 3: Update spawn_test.go**

Update existing tests to pass empty taskID, add new test:

```go
func TestBuildSpawnPrompt_NoName(t *testing.T) {
	got := buildSpawnPrompt("Fix the login bug", "abc123", "", false, "")
	// ... same expected
}

func TestBuildSpawnPrompt_WithName(t *testing.T) {
	got := buildSpawnPrompt("Fix the login bug", "abc123", "BugFixer", false, "")
	// ... same expected
}

func TestBuildSpawnPrompt_Interactive(t *testing.T) {
	got := buildSpawnPrompt("Run the game", "abc123", "", true, "")
	// ... same expected
}

func TestBuildSpawnPrompt_Delegated(t *testing.T) {
	got := buildSpawnPrompt("Write tests", "abc123", "", false, "t_deadbeef")
	if !strings.Contains(got, "t_deadbeef") {
		t.Error("should contain task ID")
	}
	if !strings.Contains(got, "report_result") {
		t.Error("should contain report_result instructions")
	}
}
```

- [ ] **Step 4: Register all 6 orchestration tools**

Add to `registerTools()` in mcp.go:

```go
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
```

- [ ] **Step 5: Implement handler functions**

```go
func (s *MCPServer) handleDelegate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.Params.Arguments
	prompt, _ := args["prompt"].(string)
	name, _ := args["name"].(string)
	cwd, _ := args["cwd"].(string)
	if cwd == "" {
		cwd = s.peerCtx.CWD
	}

	// Create task in broker
	var taskResp types.TaskCreateResponse
	if err := s.brokerPost("/task/create", types.TaskCreateRequest{
		ParentID: s.peerID,
		Prompt:   prompt,
	}, &taskResp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Spawn agent with task ID in prompt
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

	// Create task in broker with child_id already set
	var taskResp types.TaskCreateResponse
	if err := s.brokerPost("/task/create", types.TaskCreateRequest{
		ParentID: s.peerID,
		ChildID:  peerID,
		Prompt:   prompt,
	}, &taskResp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Send request message to the target peer with task_id in text
	sendReq := types.SendRequest{
		FromID: s.peerID,
		ToID:   peerID,
		Type:   types.TypeRequest,
		Text:   fmt.Sprintf("Task %s: %s", taskResp.TaskID, prompt),
	}
	s.brokerPost("/send", sendReq, nil)

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

	// task_id can be string or array
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
			if s, ok := item.(string); ok {
				taskIDs = append(taskIDs, s)
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
	// Look up the task to find the child_id
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
```

- [ ] **Step 6: Update MCP server instructions**

Update the instructions string in `NewMCPServer` to include orchestration guidance:

Add after rule 6:
```
7. Use delegate to spawn tracked agents. Use wait_for_result to collect their output.
8. When you receive a request with a task_id, call report_result with that task_id when you're done.
9. Use cancel_task after wait_for_result(mode: "any") to clean up remaining agents.
```

- [ ] **Step 7: Build and test**

Run: `go build ./... && go test ./...`
Expected: clean build, all tests pass

- [ ] **Step 8: Commit**

```bash
git add internal/server/mcp.go internal/server/spawn.go internal/server/spawn_test.go
git commit -m "feat: add orchestration tools (delegate, request_task, report_result, wait_for_result, list_tasks, cancel_task)"
```

---

### Task 6: Documentation — Rewrite README.md

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Rewrite README.md**

Full rewrite reflecting current state: single binary, in-memory broker, all MCP tools including orchestration, no CLI, correct install instructions, correct env vars.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: rewrite README for current architecture and orchestration tools"
```

---

### Task 7: Documentation — Rewrite CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Rewrite CLAUDE.md**

Fix architecture (single cmd/agentswarm-server/), build instructions (single binary), key files table (add store.go, spawn.go; remove db.go), dependencies (remove sqlite), add orchestration tools.

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: rewrite CLAUDE.md for current architecture"
```

---

### Task 8: Documentation — Rewrite SPEC.md

**Files:**
- Modify: `SPEC.md`

- [ ] **Step 1: Rewrite SPEC.md**

Fix language (Go not TypeScript), remove SQLite, fix file structure, remove threading, add spawn_agent + orchestration sections, update all endpoints.

- [ ] **Step 2: Commit**

```bash
git add SPEC.md
git commit -m "docs: rewrite SPEC.md for current architecture and orchestration"
```

---

### Task 9: CI — Add GitHub Actions workflows

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Create CI workflow**

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.24'
      - run: go build ./...
      - run: go test ./... -v
```

- [ ] **Step 2: Create release workflow**

```yaml
name: Release
on:
  push:
    tags: ['v*']

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.24'
      - run: go test ./...
      - name: Build binaries
        run: |
          mkdir -p dist
          for os in linux darwin; do
            for arch in amd64 arm64; do
              GOOS=$os GOARCH=$arch go build -o dist/agentswarm-server-${os}-${arch} ./cmd/agentswarm-server
            done
          done
      - name: Create release
        uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          files: dist/*
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "ci: add CI and release workflows"
```

---

### Task 10: Install script

**Files:**
- Create: `install.sh`

- [ ] **Step 1: Create install.sh**

Shell script that detects OS/arch, downloads from GitHub Releases, installs to ~/.local/bin with PATH warning.

- [ ] **Step 2: Make executable and commit**

```bash
chmod +x install.sh
git add install.sh
git commit -m "feat: add shell installer script"
```

---

### Task 11: Final verification

- [ ] **Step 1: Full build and test**

Run: `go build ./... && go test ./... -v`

- [ ] **Step 2: Push**

```bash
git push
```
