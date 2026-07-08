// Package serve is hydrad: the HTTP daemon behind the web dashboard,
// remote approvals, and the headless task queue.
package serve

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hydra/internal/core"
	"hydra/internal/task"
	"hydra/internal/tmux"
)

const backfillWindow = 24 * time.Hour

type server struct {
	store  *task.Store
	runner *task.Runner
	token  string
}

// Run starts the daemon. addr like "127.0.0.1:7717"; use "0.0.0.0:7717" to
// reach the dashboard from a phone on the same network.
func Run(addr string, jobs int) error {
	store, err := task.NewStore()
	if err != nil {
		return err
	}
	token, err := loadOrCreateToken()
	if err != nil {
		return err
	}
	s := &server{store: store, runner: task.NewRunner(store, jobs), token: token}
	if n := s.runner.ResumeQueued(); n > 0 {
		fmt.Printf("Resumed %d queued task(s)\n", n)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.page)
	mux.HandleFunc("GET /api/state", s.auth(s.state))
	mux.HandleFunc("GET /api/transcript", s.auth(s.transcript))
	mux.HandleFunc("POST /api/tasks", s.auth(s.createTask))
	mux.HandleFunc("POST /api/answer", s.auth(s.answer))

	fmt.Printf("hydrad listening on %s (%d task workers)\n", addr, jobs)
	fmt.Printf("Dashboard:  http://127.0.0.1:%s/?token=%s\n", port(addr), token)
	if lan := lanIP(); lan != "" && strings.HasPrefix(addr, "0.0.0.0") {
		fmt.Printf("Phone:      http://%s:%s/?token=%s\n", lan, port(addr), token)
	} else if !strings.HasPrefix(addr, "0.0.0.0") {
		fmt.Println("(for phone access restart with: hydra serve -addr 0.0.0.0:7717)")
	}
	return http.ListenAndServe(addr, mux)
}

// auth guards every API endpoint with the local token — mandatory because
// /api/answer can approve pending actions.
func (s *server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if got == "" {
			got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, `{"error":"bad or missing token"}`, http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(pageHTML))
}

type sessionJSON struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	CWD        string `json:"cwd"`
	State      string `json:"state"`
	Detail     string `json:"detail"`
	AgeSeconds int    `json:"age_seconds"`
	Tmux       bool   `json:"tmux"`
	Tracked    bool   `json:"tracked"`
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	fleet, err := core.FleetList(false, backfillWindow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sessions := make([]sessionJSON, 0, len(fleet))
	for _, f := range fleet {
		sessions = append(sessions, sessionJSON{
			ID:         f.ID,
			Project:    filepath.Base(f.CWD),
			CWD:        f.CWD,
			State:      f.State,
			Detail:     f.Detail,
			AgeSeconds: int(time.Since(f.LastSeen).Seconds()),
			Tmux:       f.TmuxPane != "",
			Tracked:    f.Tracked,
		})
	}
	writeJSON(w, map[string]any{"sessions": sessions, "tasks": s.store.List()})
}

func (s *server) transcript(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session_id")
	fleet, err := core.FleetList(true, backfillWindow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, f := range fleet {
		if f.ID == id && f.Transcript != "" {
			lines := core.Tail(f.Transcript, 64<<10)
			if len(lines) > 40 {
				lines = lines[len(lines)-40:]
			}
			out := make([]map[string]string, 0, len(lines))
			for _, ln := range lines {
				out = append(out, map[string]string{"kind": ln.Kind, "text": ln.Text})
			}
			writeJSON(w, map[string]any{"lines": out})
			return
		}
	}
	writeJSON(w, map[string]any{"lines": []any{}})
}

func (s *server) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt         string `json:"prompt"`
		Dir            string `json:"dir"`
		PermissionMode string `json:"permission_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
		return
	}
	if req.Dir == "" {
		req.Dir, _ = os.UserHomeDir()
	}
	if fi, err := os.Stat(req.Dir); err != nil || !fi.IsDir() {
		http.Error(w, `{"error":"dir does not exist: `+req.Dir+`"}`, http.StatusBadRequest)
		return
	}
	t := task.NewTask(req.Prompt, req.Dir, req.PermissionMode)
	if err := s.store.Save(t); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.runner.Enqueue(t.ID)
	writeJSON(w, t)
}

// answer approves or rejects a pending permission prompt in a tmux-hosted
// session — the same screen-verified path the TUI uses, now reachable from
// a phone.
func (s *server) answer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Approve   bool   `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	fleet, err := core.FleetList(false, backfillWindow)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, f := range fleet {
		if f.ID != req.SessionID {
			continue
		}
		switch {
		case f.TmuxPane == "":
			writeJSON(w, map[string]any{"ok": false, "message": "session is not in tmux — cannot answer remotely"})
		case !tmux.PaneExists(f.TmuxPane):
			writeJSON(w, map[string]any{"ok": false, "message": "tmux pane is gone"})
		case !tmux.PermissionVisible(f.TmuxPane):
			writeJSON(w, map[string]any{"ok": false, "message": "no permission dialog on screen — nothing sent"})
		default:
			key, verb := "1", "approved"
			if !req.Approve {
				key, verb = "Escape", "rejected"
			}
			if err := tmux.SendKeys(f.TmuxPane, key); err != nil {
				writeJSON(w, map[string]any{"ok": false, "message": "send failed: " + err.Error()})
				return
			}
			writeJSON(w, map[string]any{"ok": true, "message": verb})
		}
		return
	}
	writeJSON(w, map[string]any{"ok": false, "message": "unknown session"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func loadOrCreateToken() (string, error) {
	path := filepath.Join(core.HydraDir(), "token")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 16 {
		return strings.TrimSpace(string(data)), nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(core.HydraDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func port(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return "7717"
}

func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return ""
}
