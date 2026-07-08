// Package task is hydra's headless work queue: prompts executed by
// 'claude -p --output-format stream-json' under a worker pool.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"hydra/internal/core"
)

type Task struct {
	ID             string    `json:"id"`
	Prompt         string    `json:"prompt"`
	Dir            string    `json:"dir"`
	PermissionMode string    `json:"permission_mode"` // default | acceptEdits | plan | bypassPermissions
	Status         string    `json:"status"`          // queued | running | done | failed
	SessionID      string    `json:"session_id,omitempty"`
	Result         string    `json:"result,omitempty"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
}

func NewTask(prompt, dir, mode string) *Task {
	b := make([]byte, 4)
	rand.Read(b)
	if mode == "" {
		mode = "acceptEdits"
	}
	return &Task{
		ID:             time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b),
		Prompt:         prompt,
		Dir:            dir,
		PermissionMode: mode,
		Status:         "queued",
		CreatedAt:      time.Now(),
	}
}

// Store keeps one JSON file per task plus a raw stream log, under
// ~/.hydra/tasks — durable across daemon restarts and easy to inspect.
type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore() (*Store, error) {
	dir := filepath.Join(core.HydraDir(), "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(id string) string    { return filepath.Join(s.dir, id+".json") }
func (s *Store) LogPath(id string) string { return filepath.Join(s.dir, id+".log") }

func (s *Store) Save(t *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(t.ID), append(data, '\n'), 0o644)
}

func (s *Store) Get(id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// List returns all tasks, newest first.
func (s *Store) List() []Task {
	paths, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	tasks := make([]Task, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var t Task
		if json.Unmarshal(data, &t) == nil {
			tasks = append(tasks, t)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	return tasks
}
