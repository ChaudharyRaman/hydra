// Package core holds hydra's shared model: the event log written by hooks,
// the fleet state folded from it, and Claude Code transcript parsing.
package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Event is one line in ~/.hydra/events.jsonl.
type Event struct {
	TS         time.Time `json:"ts"`
	Host       string    `json:"host"`
	Event      string    `json:"event"`
	SessionID  string    `json:"session_id"`
	CWD        string    `json:"cwd,omitempty"`
	Transcript string    `json:"transcript_path,omitempty"`
	Message    string    `json:"message,omitempty"`
	Tool       string    `json:"tool_name,omitempty"`
	TmuxPane   string    `json:"tmux_pane,omitempty"` // %id when the session runs inside tmux
	HeadID     string    `json:"head_id,omitempty"`   // hydra head this session belongs to (embedded terminal)
}

func HydraDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra")
}

func EventsPath() string { return filepath.Join(HydraDir(), "events.jsonl") }

func ProjectsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// Append writes one JSONL line. O_APPEND writes under 4KB are atomic on
// Linux, so concurrent sessions can share the file without locking.
func Append(ev Event) {
	if os.MkdirAll(HydraDir(), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(EventsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f.Write(append(line, '\n'))
}
