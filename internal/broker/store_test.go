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

func TestWaitForTaskAnyMode(t *testing.T) {
	s := NewStore()
	id1 := s.CreateTask("parent1", "", "task A")
	id2 := s.CreateTask("parent1", "", "task B")

	done := make(chan []types.TaskResult)
	go func() {
		results := s.WaitForTasks([]string{id1, id2}, "any", 5*time.Second)
		done <- results
	}()

	time.Sleep(50 * time.Millisecond)
	s.UpdateTask(id1, "child1", "completed", "first!")

	results := <-done
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	var foundCompleted bool
	for _, r := range results {
		if r.Status == "completed" {
			foundCompleted = true
		}
	}
	if !foundCompleted {
		t.Error("expected at least one completed result")
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

func TestResultSizeLimit(t *testing.T) {
	s := NewStore()
	id := s.CreateTask("parent1", "", "do something")
	bigResult := make([]byte, 100*1024) // 100KB
	for i := range bigResult {
		bigResult[i] = 'x'
	}
	s.UpdateTask(id, "child1", "completed", string(bigResult))
	task, _ := s.GetTask(id)
	if len(task.Result) > maxResultSize {
		t.Errorf("result should be capped at %d, got %d", maxResultSize, len(task.Result))
	}
}
