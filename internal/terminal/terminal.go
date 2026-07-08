// Package terminal owns the live heads: each is a child process running
// under a PTY that hydra allocates, with a virtual-terminal emulator that
// turns its byte stream into a renderable screen. This is what lets the
// dashboard show a real, typeable terminal in its right pane.
package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Session is one live head.
type Session struct {
	ID      string
	Name    string
	Dir     string
	Started time.Time

	cmd  *exec.Cmd
	ptmx *os.File
	em   *vt.SafeEmulator

	mu    sync.Mutex
	cols  int
	rows  int
	alive bool
	exit  string // set once the process exits
}

func newID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// shellCommand builds the interactive shell for a head, per platform.
func shellCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		if ps, err := exec.LookPath("pwsh.exe"); err == nil {
			return exec.Command(ps)
		}
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "powershell.exe"
		}
		return exec.Command(comspec)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	return exec.Command(shell, "-i")
}

// New spawns a head: an interactive login shell in dir, stamped with
// HYDRA_HEAD_ID so the hook can tie this pane's Claude session back to its
// status badge. autorun (e.g. "claude") is typed in once the shell is up,
// so the head survives Claude restarts and falls back to a usable shell.
func New(name, dir, autorun string, cols, rows int) (*Session, error) {
	if cols < 20 {
		cols = 80
	}
	if rows < 5 {
		rows = 24
	}
	id := newID()

	cmd := shellCommand()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"HYDRA_HEAD_ID="+id,
		"HYDRA_HEAD_NAME="+name,
	)
	if runtime.GOOS != "windows" {
		// make sure claude + hydra resolve even from a bare login shell
		home, _ := os.UserHomeDir()
		cmd.Env = append(cmd.Env, "PATH="+home+"/.local/bin:"+home+"/.hydra/bin:"+os.Getenv("PATH"))
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	s := &Session{
		ID: id, Name: name, Dir: dir, Started: time.Now(),
		cmd: cmd, ptmx: ptmx, em: vt.NewSafeEmulator(cols, rows),
		cols: cols, rows: rows, alive: true,
	}

	go io.Copy(s.em, ptmx) // child output -> emulator screen
	go io.Copy(ptmx, s.em) // emulator responses -> child (cursor reports, etc.)
	go s.wait()

	if autorun != "" {
		go func() {
			time.Sleep(250 * time.Millisecond)
			s.Send([]byte(autorun + "\n"))
		}()
	}
	return s, nil
}

func (s *Session) wait() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.alive = false
	if err != nil {
		s.exit = err.Error()
	} else {
		s.exit = "exited"
	}
	s.mu.Unlock()
}

// Render returns the head's current screen, styled with ANSI.
func (s *Session) Render() string { return s.em.Render() }

// CursorPos returns the emulator's cursor cell (column, row).
func (s *Session) CursorPos() (int, int) {
	p := s.em.CursorPosition()
	return p.X, p.Y
}

// ScrollbackLen is the number of lines that have scrolled off the top and
// are available to scroll back into view.
func (s *Session) ScrollbackLen() int { return s.em.ScrollbackLen() }

// ViewLines returns `rows` display lines for a view scrolled `offset` lines
// up from the live bottom. offset 0 == the current screen. Scrollback lines
// are rendered above the live screen, forming one continuous history.
func (s *Session) ViewLines(offset, rows int) []string {
	sbLen := s.em.ScrollbackLen()
	screen := strings.Split(s.em.Render(), "\n")
	total := sbLen + len(screen)
	if offset < 0 {
		offset = 0
	}
	if offset > sbLen {
		offset = sbLen
	}
	end := total - offset
	start := end - rows
	if start < 0 {
		start = 0
	}
	s.mu.Lock()
	cols := s.cols
	s.mu.Unlock()

	out := make([]string, 0, rows)
	for i := start; i < end; i++ {
		if i < sbLen {
			out = append(out, s.scrollbackLine(i, cols))
		} else if i-sbLen < len(screen) {
			out = append(out, screen[i-sbLen])
		}
	}
	return out
}

// BufferLines returns the whole history (scrollback + live screen) as plain
// text, one string per line — for search. Oldest line first; index N-1 is
// the bottom of the live screen.
func (s *Session) BufferLines() []string {
	sbLen := s.em.ScrollbackLen()
	s.mu.Lock()
	cols := s.cols
	s.mu.Unlock()

	out := make([]string, 0, sbLen+len(strings.Split(s.em.Render(), "\n")))
	for i := 0; i < sbLen; i++ {
		out = append(out, ansi.Strip(s.scrollbackLine(i, cols)))
	}
	for _, l := range strings.Split(s.em.Render(), "\n") {
		out = append(out, ansi.Strip(l))
	}
	return out
}

// scrollbackLine renders one scrollback row via the emulator's guarded
// cell accessor (safe against the concurrent output copier).
func (s *Session) scrollbackLine(y, cols int) string {
	var b strings.Builder
	for x := 0; x < cols; x++ {
		c := s.em.ScrollbackCellAt(x, y)
		if c == nil || c.Content == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Style.Styled(c.Content))
		}
	}
	return b.String()
}

// Send writes raw bytes to the head's input (keystrokes).
func (s *Session) Send(b []byte) {
	s.mu.Lock()
	alive := s.alive
	s.mu.Unlock()
	if alive {
		s.ptmx.Write(b)
	}
}

// Resize matches the PTY and emulator to a new pane size.
func (s *Session) Resize(cols, rows int) {
	if cols < 20 || rows < 3 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols == s.cols && rows == s.rows {
		return
	}
	s.cols, s.rows = cols, rows
	pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	s.em.Resize(cols, rows)
}

func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

// DisplayName returns the head's current name (safe against Rename).
func (s *Session) DisplayName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Name
}

// Rename changes the head's sidebar label.
func (s *Session) Rename(n string) {
	n = strings.TrimSpace(n)
	if n == "" {
		return
	}
	s.mu.Lock()
	s.Name = n
	s.mu.Unlock()
}

// Close terminates the head.
func (s *Session) Close() {
	s.mu.Lock()
	alive := s.alive
	s.mu.Unlock()
	if alive && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.ptmx.Close()
}

// Manager holds every live head hydra has spawned, in creation order.
type Manager struct {
	mu    sync.Mutex
	order []string
	byID  map[string]*Session
}

func NewManager() *Manager { return &Manager{byID: map[string]*Session{}} }

func (m *Manager) Spawn(name, dir, autorun string, cols, rows int) (*Session, error) {
	s, err := New(name, dir, autorun, cols, rows)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.byID[s.ID] = s
	m.order = append(m.order, s.ID)
	m.mu.Unlock()
	return s, nil
}

func (m *Manager) List() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.byID[id])
	}
	return out
}

func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

func (m *Manager) CloseAll() {
	for _, s := range m.List() {
		s.Close()
	}
}
