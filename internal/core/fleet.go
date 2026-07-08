package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session is one Claude Code session as hydra understands it.
type Session struct {
	ID         string
	CWD        string
	State      string // needs-you | working | done | started | idle | ended
	Detail     string
	Transcript string
	TmuxPane   string // %id when the session runs inside tmux — hydra can control it
	HeadID     string // hydra head this session belongs to (embedded terminal)
	LastSeen   time.Time
	Tracked    bool // true = state comes from hook events; false = backfilled from transcript mtime
}

// StatesByHead maps head_id -> the latest folded session state, so the TUI
// can badge a live embedded terminal with its Claude status.
func StatesByHead() map[string]*Session {
	sessions, err := FoldEvents()
	if err != nil {
		return nil
	}
	out := map[string]*Session{}
	for _, s := range sessions {
		if s.HeadID == "" {
			continue
		}
		if prev, ok := out[s.HeadID]; !ok || s.LastSeen.After(prev.LastSeen) {
			out[s.HeadID] = s
		}
	}
	return out
}

// StateRank orders the fleet: whatever needs a human comes first.
var StateRank = map[string]int{
	"needs-you": 0, "working": 1, "done": 2, "started": 3, "idle": 4, "ended": 5,
}

// FoldEvents replays the event log into one state per session.
// A missing log is an empty fleet, not an error.
func FoldEvents() (map[string]*Session, error) {
	sessions := map[string]*Session{}
	f, err := os.Open(EventsPath())
	if os.IsNotExist(err) {
		return sessions, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.SessionID == "" {
			continue
		}
		s := sessions[ev.SessionID]
		if s == nil {
			s = &Session{ID: ev.SessionID, Tracked: true}
			sessions[ev.SessionID] = s
		}
		s.LastSeen = ev.TS
		if ev.CWD != "" {
			s.CWD = ev.CWD
		}
		if ev.Transcript != "" {
			s.Transcript = ev.Transcript
		}
		if ev.TmuxPane != "" {
			s.TmuxPane = ev.TmuxPane
		}
		if ev.HeadID != "" {
			s.HeadID = ev.HeadID
		}
		switch ev.Event {
		case "SessionStart":
			s.State, s.Detail = "started", ""
		case "UserPromptSubmit":
			s.State, s.Detail = "working", ""
		case "PreToolUse":
			s.State, s.Detail = "working", "running "+ev.Tool
		case "Notification":
			s.State, s.Detail = "needs-you", ev.Message
		case "Stop":
			s.State, s.Detail = "done", ""
		case "SessionEnd":
			s.State, s.Detail = "ended", ""
		}
	}
	return sessions, sc.Err()
}

// Backfill adds sessions found in ~/.claude/projects that have no hook
// events (started before hydra was installed). They appear as "idle" with
// age taken from the transcript's mtime. Only transcripts modified within
// the window are included, to keep months of history out of the fleet view.
func Backfill(sessions map[string]*Session, window time.Duration) {
	paths, err := filepath.Glob(filepath.Join(ProjectsDir(), "*", "*.jsonl"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-window)
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if s, ok := sessions[id]; ok {
			if s.Transcript == "" {
				s.Transcript = p
			}
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || fi.ModTime().Before(cutoff) || fi.Size() == 0 {
			continue
		}
		sessions[id] = &Session{
			ID:         id,
			CWD:        transcriptCWD(p),
			State:      "idle",
			Detail:     "(pre-hydra session; restart to track live)",
			Transcript: p,
			LastSeen:   fi.ModTime(),
		}
	}
}

// transcriptCWD digs the session's working directory out of the first few
// transcript lines (each Claude Code transcript line carries a "cwd" field).
func transcriptCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for i := 0; i < 20 && sc.Scan(); i++ {
		var line struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.CWD != "" {
			return line.CWD
		}
	}
	return ""
}

// FleetList returns the current fleet, urgent first.
func FleetList(includeEnded bool, backfillWindow time.Duration) ([]Session, error) {
	sessions, err := FoldEvents()
	if err != nil {
		return nil, err
	}
	if backfillWindow > 0 {
		Backfill(sessions, backfillWindow)
	}
	list := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		if s.State == "ended" && !includeEnded {
			continue
		}
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool {
		if StateRank[list[i].State] != StateRank[list[j].State] {
			return StateRank[list[i].State] < StateRank[list[j].State]
		}
		return list[i].LastSeen.After(list[j].LastSeen)
	})
	return list, nil
}
