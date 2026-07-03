package tools

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// Task is the in-memory task model.
type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Status      string         `json:"status"` // pending, in_progress, completed
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskStore manages tasks per thread. Thread-safe.
type TaskStore struct {
	tasks map[string]*Task // keyed by task ID
	mu    sync.RWMutex
	seq   atomic.Int64
}

// NewTaskStore creates an empty task store.
func NewTaskStore() *TaskStore {
	return &TaskStore{
		tasks: make(map[string]*Task),
	}
}

// Create adds a new task and returns its ID.
func (ts *TaskStore) Create(subject, description, activeForm string) *Task {
	id := fmt.Sprintf("%d", ts.seq.Add(1))
	t := &Task{
		ID:          id,
		Subject:     subject,
		Description: description,
		ActiveForm:  activeForm,
		Status:      "pending",
	}
	ts.mu.Lock()
	ts.tasks[id] = t
	ts.mu.Unlock()
	return t
}

// Get retrieves a task by ID.
func (ts *TaskStore) Get(id string) *Task {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.tasks[id]
}

// Update modifies a task. Returns false if not found.
func (ts *TaskStore) Update(id string, status, owner, subject, description string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.tasks[id]
	if !ok {
		return false
	}
	if status != "" {
		t.Status = status
	}
	if owner != "" {
		t.Owner = owner
	}
	if subject != "" {
		t.Subject = subject
	}
	if description != "" {
		t.Description = description
	}
	return true
}

// Delete removes a task.
func (ts *TaskStore) Delete(id string) {
	ts.mu.Lock()
	delete(ts.tasks, id)
	ts.mu.Unlock()
}

// List returns all tasks.
func (ts *TaskStore) List() []*Task {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]*Task, 0, len(ts.tasks))
	for _, t := range ts.tasks {
		result = append(result, t)
	}
	return result
}

// MarshalJSON serializes all tasks.
func (ts *TaskStore) MarshalJSON() ([]byte, error) {
	return json.Marshal(ts.List())
}
