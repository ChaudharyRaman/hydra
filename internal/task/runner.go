package task

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"hydra/internal/notify"
)

// Runner executes queued tasks with a fixed pool of workers, each running
// one headless Claude Code process at a time.
type Runner struct {
	store *Store
	queue chan string
}

func NewRunner(store *Store, jobs int) *Runner {
	if jobs < 1 {
		jobs = 1
	}
	r := &Runner{store: store, queue: make(chan string, 256)}
	for i := 0; i < jobs; i++ {
		go r.worker()
	}
	return r
}

// ResumeQueued re-enqueues tasks that were queued (or orphaned mid-run by a
// daemon restart) so no accepted work is ever lost.
func (r *Runner) ResumeQueued() int {
	n := 0
	for _, t := range r.store.List() {
		if t.Status == "queued" || t.Status == "running" {
			t.Status = "queued"
			r.store.Save(&t)
			r.Enqueue(t.ID)
			n++
		}
	}
	return n
}

func (r *Runner) Enqueue(id string) {
	select {
	case r.queue <- id:
	default:
		// Queue channel full: leave status "queued" on disk; a restart or
		// later drain will pick it up.
	}
}

func (r *Runner) worker() {
	for id := range r.queue {
		r.execute(id)
	}
}

// streamLine covers the claude -p stream-json lines hydra cares about.
type streamLine struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	SessionID    string  `json:"session_id"`
	Result       string  `json:"result"`
	IsError      bool    `json:"is_error"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

func (r *Runner) execute(id string) {
	t, err := r.store.Get(id)
	if err != nil || t.Status == "done" || t.Status == "failed" {
		return
	}
	t.Status, t.StartedAt = "running", time.Now()
	r.store.Save(t)

	logf, err := os.Create(r.store.LogPath(t.ID))
	if err != nil {
		r.finish(t, "failed", "cannot create log: "+err.Error())
		return
	}
	defer logf.Close()

	bin := os.Getenv("HYDRA_CLAUDE_CMD")
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.Command(bin, "-p", t.Prompt,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", t.PermissionMode)
	cmd.Dir = t.Dir
	cmd.Stderr = logf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.finish(t, "failed", err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		r.finish(t, "failed", err.Error())
		return
	}

	var isError bool
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 4<<20), 4<<20)
	for sc.Scan() {
		logf.Write(append(sc.Bytes(), '\n'))
		var line streamLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.SessionID != "" && t.SessionID == "" {
			t.SessionID = line.SessionID
			r.store.Save(t)
		}
		if line.Type == "result" {
			t.Result = line.Result
			t.CostUSD = line.TotalCostUSD
			isError = line.IsError || line.Subtype != "success"
		}
	}
	err = cmd.Wait()

	status := "done"
	if err != nil || isError {
		status = "failed"
	}
	r.finish(t, status, t.Result)
}

func (r *Runner) finish(t *Task, status, result string) {
	t.Status, t.FinishedAt = status, time.Now()
	if t.Result == "" {
		t.Result = result
	}
	r.store.Save(t)

	project := filepath.Base(t.Dir)
	if status == "done" {
		notify.Send("✅ task done — "+project, firstLine(t.Prompt), false)
	} else {
		notify.Send("🔴 task FAILED — "+project, firstLine(t.Prompt), true)
	}
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
