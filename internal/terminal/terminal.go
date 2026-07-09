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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// osc8Re matches an OSC 8 hyperlink sequence: ESC ]8; <params> ; <uri> BEL.
// The vt emulator parses the params/uri fields swapped and re-emits them the
// same way, so a link like "https://x" comes out with the URI set to the
// params (e.g. "id=abc"). Clicking it in the outer terminal then tries to open
// the params string. renderScreen swaps the two fields back.
var osc8Re = regexp.MustCompile("\x1b\\]8;([^;\x1b\x07]*);([^\x1b\x07]*)\x07")

// fixHyperlinks restores correct OSC 8 field order (params;uri) in emulator
// output. No-op when the frame has no hyperlinks.
func fixHyperlinks(s string) string {
	if !strings.Contains(s, "\x1b]8;") {
		return s
	}
	return osc8Re.ReplaceAllString(s, "\x1b]8;${2};${1}\x07")
}

// renderInterval is how often a head refreshes its cached screen snapshot.
const renderInterval = 33 * time.Millisecond

// Session is one live head. Rendering is decoupled from the UI thread: the
// emulator (heavily write-locked by streaming child output) is only touched
// by the head's own goroutines, which publish a cached snapshot the UI reads
// lock-free. Input is queued so a busy child can never block the caller.
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

	// Cached render snapshot, read by the UI thread without touching the
	// emulator lock.
	cacheMu    sync.RWMutex
	cache      string
	curX, curY int

	dirty   atomic.Bool
	inputCh chan []byte
	done    chan struct{}
	closeCh sync.Once
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
		inputCh: make(chan []byte, 4096),
		done:    make(chan struct{}),
	}
	s.dirty.Store(true)

	go s.readLoop()        // child output -> emulator screen (+ mark dirty)
	go io.Copy(ptmx, s.em) // emulator responses -> child (cursor reports, etc.)
	go s.writeLoop()       // queued input -> child (never blocks the caller)
	go s.renderLoop()      // throttled emulator snapshot -> cache
	go s.wait()

	if autorun != "" {
		go func() {
			time.Sleep(250 * time.Millisecond)
			s.Send([]byte(autorun + "\n"))
		}()
	}
	return s, nil
}

// readLoop copies child output into the emulator and flags the screen dirty.
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.em.Write(buf[:n])
			s.dirty.Store(true)
		}
		if err != nil {
			return
		}
	}
}

// writeLoop drains queued keystrokes to the PTY. Isolating the write here
// means a child that has stopped reading stdin blocks only this goroutine,
// never the UI thread.
func (s *Session) writeLoop() {
	for {
		select {
		case b := <-s.inputCh:
			s.ptmx.Write(b)
		case <-s.done:
			return
		}
	}
}

// renderLoop republishes the cached screen snapshot at a steady rate, but
// only when the emulator has actually changed.
func (s *Session) renderLoop() {
	t := time.NewTicker(renderInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if s.dirty.Swap(false) {
				s.updateCache()
			}
		case <-s.done:
			s.updateCache() // final frame
			return
		}
	}
}

// renderScreen renders the live screen with hyperlink fields corrected.
func (s *Session) renderScreen() string { return fixHyperlinks(s.em.Render()) }

func (s *Session) updateCache() {
	r := s.renderScreen()
	p := s.em.CursorPosition()
	s.cacheMu.Lock()
	s.cache, s.curX, s.curY = r, p.X, p.Y
	s.cacheMu.Unlock()
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
	s.closeCh.Do(func() { close(s.done) })
}

// Render returns the head's cached screen snapshot (styled with ANSI). This
// is lock-free with respect to the emulator, so heavy child output never
// stalls the UI thread that calls it.
func (s *Session) Render() string {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return s.cache
}

// CursorPos returns the cached cursor cell (column, row).
func (s *Session) CursorPos() (int, int) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return s.curX, s.curY
}

// ScrollbackLen is the number of lines that have scrolled off the top and
// are available to scroll back into view.
func (s *Session) ScrollbackLen() int { return s.em.ScrollbackLen() }

// IsAltScreen reports whether the child is on the alternate screen (a
// full-screen TUI), where there is no scrollback to move into.
func (s *Session) IsAltScreen() bool { return s.em.IsAltScreen() }

// ViewLines returns `rows` display lines for a view scrolled `offset` lines
// up from the live bottom. offset 0 == the current screen. Scrollback lines
// are rendered above the live screen, forming one continuous history.
func (s *Session) ViewLines(offset, rows int) []string {
	sbLen := s.em.ScrollbackLen()
	screen := strings.Split(s.renderScreen(), "\n")
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

// Send queues raw bytes to the head's input (keystrokes). Non-blocking: it
// returns immediately even if the child is momentarily not reading stdin.
func (s *Session) Send(b []byte) {
	if !s.Alive() {
		return
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	select {
	case s.inputCh <- cp:
	case <-s.done:
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
	s.closeCh.Do(func() { close(s.done) })
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
