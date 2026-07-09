package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"hydra/internal/clipboard"
	"hydra/internal/core"
	"hydra/internal/terminal"
	"hydra/internal/update"
)

// ansiTrunc truncates to a visible width while preserving ANSI styling.
func ansiTrunc(s string, w int) string { return ansi.Truncate(s, w, "") }

// cursorOverlay renders a reverse-video block at visible column col of an
// already-styled line, so the embedded terminal shows where typing lands.
func cursorOverlay(line string, col int) string {
	if col < 0 {
		return line
	}
	width := lipgloss.Width(line)
	if col >= width {
		// Cursor sits at or past the end of the rendered line — e.g. just
		// after a trailing space the emulator trimmed. Pad out with spaces so
		// the block lands at the true cursor column instead of snapping back.
		return line + strings.Repeat(" ", col-width) + "\x1b[0m\x1b[7m \x1b[0m"
	}
	ch := ansi.Strip(ansi.Cut(line, col, col+1))
	if ch == "" {
		ch = " "
	}
	return ansi.Cut(line, 0, col) + "\x1b[0m\x1b[7m" + ch + "\x1b[0m" + ansi.Cut(line, col+1, width)
}

// The console is hydra's primary view: a sidebar of sessions (PROJECTS &
// AGENTS) beside the live, typeable terminal of the selected head.

const sidebarW = 32

const (
	modeClaude = iota // head runs Claude Code
	modeShell         // head runs a plain interactive shell
	modeCustom        // head runs an arbitrary command (ssh, a REPL, …)
)

var modeName = map[int]string{modeClaude: "Claude", modeShell: "plain shell", modeCustom: "custom command"}

type consoleTick time.Time

type item struct {
	kind    string // "head" = live embedded terminal, "external" = monitor-only
	id      string
	name    string
	dir     string
	state   string
	ageSecs int
	live    bool
	head    *terminal.Session
}

type consoleModel struct {
	mgr       *terminal.Manager
	items     []item
	selected  int
	focusTerm bool // keystrokes go to the terminal instead of the sidebar

	prompting  bool // Ctrl+N new-agent prompt
	renaming   bool // R rename prompt
	input      string
	promptMode int      // modeClaude | modeShell | modeCustom
	promptSel  int      // -1 = the typed input line; 0..n-1 = a saved path
	completes  []string // directory candidates shown after Tab

	savedPaths []string  // pinned project directories
	quitAt     time.Time // for double-press quit

	scrollOff int  // lines scrolled up into the focused head's history (0 = live)
	lastSbLen int  // focused head's scrollback length at the last tick (drift anchor)
	helpOpen  bool // F1 help overlay

	// Mouse text selection over the terminal pane (content-row/col coords).
	selActive    bool
	selAX, selAY int // anchor
	selBX, selBY int // current end

	// Scrollback search.
	searching   bool   // typing a query
	searchQuery string // applied query (highlighted)
	matches     []int  // buffer line indices containing the query
	matchIdx    int
	searchTotal int // buffer length when the search ran (for jump mapping)

	flash      string
	flashUntil time.Time
	newVersion string // set if a newer release is available
	width      int
	height     int
	ticks      int
}

type updateAvailableMsg string

// checkUpdateCmd checks (throttled to once/day) whether a newer release
// exists, off the UI thread so startup stays instant.
func checkUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		latest, err := update.LatestCachedOrFetch(ctx)
		if err != nil || !update.IsNewer(version, latest) {
			return nil
		}
		return updateAvailableMsg(latest)
	}
}

func runConsole() {
	m := &consoleModel{mgr: terminal.NewManager()}
	m.loadSaved()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	m.mgr.CloseAll()
}

func (m *consoleModel) setFlash(s string) {
	m.flash = s
	m.flashUntil = time.Now().Add(4 * time.Second)
}

func (m *consoleModel) loadSaved() { m.savedPaths = core.SavedPaths() }

// expandHome resolves a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// completeDir does shell-style Tab completion over directories. It returns
// the completed input and, when ambiguous, the list of candidate names.
func completeDir(in string) (string, []string) {
	full := expandHome(in)
	dir, base := filepath.Dir(full), filepath.Base(full)
	if strings.HasSuffix(full, "/") || full == "" {
		dir, base = strings.TrimSuffix(full, "/"), ""
		if dir == "" {
			dir = "/"
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return in, nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), base) && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	switch len(names) {
	case 0:
		return in, nil
	case 1:
		return filepath.Join(dir, names[0]) + "/", nil
	default:
		return filepath.Join(dir, commonPrefix(names)), names
	}
}

func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

func (m *consoleModel) Init() tea.Cmd { return tea.Batch(consoleTickCmd(), checkUpdateCmd()) }

func consoleTickCmd() tea.Cmd {
	// Cheap now that Render() reads a cached snapshot, so poll often for
	// smooth motion; Bubble Tea only writes cells that actually changed.
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg { return consoleTick(t) })
}

// ---- sizing ----

func (m *consoleModel) bodyHeight() int { return max(m.height-2, 4) } // minus title + footer
func (m *consoleModel) rightW() int     { return max(m.width-sidebarW, 24) }
func (m *consoleModel) termCols() int   { return max(m.rightW()-2, 20) } // minus box border
func (m *consoleModel) termRows() int   { return max(m.height-5, 3) }    // title+footer+border+header

// ---- update ----

func (m *consoleModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeFocused()
	case consoleTick:
		m.ticks++
		if m.ticks%6 == 0 { // ~every half-second: refresh sidebar states
			m.refresh()
		}
		m.anchorScroll()
		m.resizeFocused()
		return m, consoleTickCmd()
	case updateAvailableMsg:
		m.newVersion = string(msg)
	case tea.MouseMsg:
		m.onMouse(msg)
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// onMouse handles wheel scrolling and click-drag text selection inside the
// terminal pane (hydra captures the mouse, so selection is done in-app).
func (m *consoleModel) onMouse(msg tea.MouseMsg) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scrollBy(3)
		return
	case tea.MouseButtonWheelDown:
		m.scrollBy(-3)
		return
	case tea.MouseButtonLeft:
	default:
		return
	}

	col := msg.X - (sidebarW + 1) // right box: left border + 1
	row := msg.Y - 3              // title + top border + header
	switch msg.Action {
	case tea.MouseActionPress:
		if col >= 0 && col < m.termCols() && row >= 0 && row < m.termRows() {
			m.selActive = true
			m.selAX, m.selAY, m.selBX, m.selBY = col, row, col, row
		} else {
			m.selActive = false
		}
	case tea.MouseActionMotion:
		if m.selActive {
			m.selBX, m.selBY = clamp(col, 0, m.termCols()-1), clamp(row, 0, m.termRows()-1)
		}
	case tea.MouseActionRelease:
		if m.selActive {
			m.copySelection()
		}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// copySelection extracts the selected text from the visible lines and copies
// it to the system clipboard.
func (m *consoleModel) copySelection() {
	lines := m.visibleTextLines()
	ax, ay, bx, by := m.selAX, m.selAY, m.selBX, m.selBY
	if ay > by || (ay == by && ax > bx) {
		ax, ay, bx, by = bx, by, ax, ay
	}
	var parts []string
	for y := ay; y <= by && y < len(lines); y++ {
		line := lines[y]
		from, to := 0, len([]rune(line))
		if y == ay {
			from = ax
		}
		if y == by {
			to = bx + 1
		}
		parts = append(parts, runeSlice(line, from, to))
	}
	text := strings.TrimRight(strings.Join(parts, "\n"), " \n")
	if text != "" {
		clipboard.Copy(text)
		m.setFlash(fmt.Sprintf("copied %d chars to clipboard", len(text)))
	}
}

func runeSlice(s string, from, to int) string {
	r := []rune(s)
	if from < 0 {
		from = 0
	}
	if to > len(r) {
		to = len(r)
	}
	if from >= to {
		return ""
	}
	return string(r[from:to])
}

// visibleLines returns the styled content rows currently shown for the
// selected head (no header, no cursor overlay) — the single source both the
// renderer and the selection/copy path read from, so they never diverge.
func (m *consoleModel) visibleLines() []string {
	it := m.cur()
	if it == nil || it.head == nil {
		return nil
	}
	if m.scrollOff > 0 {
		return it.head.ViewLines(m.scrollOff, m.termRows())
	}
	return strings.Split(it.head.Render(), "\n")
}

func (m *consoleModel) visibleTextLines() []string {
	styled := m.visibleLines()
	out := make([]string, len(styled))
	for i, l := range styled {
		out[i] = ansi.Strip(l)
	}
	return out
}

// runSearch finds every buffer line containing the query and jumps to the
// most recent (bottom-most) match — reverse-search order.
func (m *consoleModel) runSearch() {
	m.matches, m.matchIdx = nil, 0
	it := m.cur()
	if it == nil || it.head == nil || strings.TrimSpace(m.searchQuery) == "" {
		return
	}
	buf := it.head.BufferLines()
	m.searchTotal = len(buf)
	q := strings.ToLower(m.searchQuery)
	for i, line := range buf {
		if strings.Contains(strings.ToLower(line), q) {
			m.matches = append(m.matches, i)
		}
	}
	if len(m.matches) == 0 {
		m.setFlash("no match for '" + m.searchQuery + "'")
		return
	}
	m.matchIdx = len(m.matches) - 1
	m.jumpToMatch()
	m.setFlash(fmt.Sprintf("match %d/%d — n older · N newer", m.matchIdx+1, len(m.matches)))
}

func (m *consoleModel) stepMatch(dir int) {
	if len(m.matches) == 0 {
		return
	}
	m.matchIdx = clamp(m.matchIdx+dir, 0, len(m.matches)-1)
	m.jumpToMatch()
	m.setFlash(fmt.Sprintf("match %d/%d", m.matchIdx+1, len(m.matches)))
}

// jumpToMatch scrolls so the current match sits near the middle of the pane.
func (m *consoleModel) jumpToMatch() {
	it := m.cur()
	if it == nil || it.head == nil || len(m.matches) == 0 {
		return
	}
	line := m.matches[m.matchIdx]
	off := m.searchTotal - line - m.termRows()/2
	m.scrollOff = clamp(off, 0, it.head.ScrollbackLen())
	m.lastSbLen = it.head.ScrollbackLen() // re-baseline drift anchor to this jump
}

// decorate applies search highlight, selection highlight, and the cursor to
// one styled content row (in that order).
func (m *consoleModel) decorate(line string, row, cursorCol int) string {
	if m.searchQuery != "" {
		line = highlightAll(line, m.searchQuery)
	}
	if m.selActive {
		if from, to, ok := m.selRangeForRow(row); ok {
			line = highlightRange(line, from, to)
		}
	}
	if cursorCol >= 0 {
		line = cursorOverlay(line, cursorCol)
	}
	return line
}

func (m *consoleModel) selRangeForRow(row int) (from, to int, ok bool) {
	ax, ay, bx, by := m.selAX, m.selAY, m.selBX, m.selBY
	if ay > by || (ay == by && ax > bx) {
		ax, ay, bx, by = bx, by, ax, ay
	}
	if row < ay || row > by {
		return 0, 0, false
	}
	from, to = 0, m.termCols()
	if row == ay {
		from = ax
	}
	if row == by {
		to = bx + 1
	}
	return from, to, true
}

// highlightRange reverse-videos visible columns [from,to) of a styled line.
func highlightRange(line string, from, to int) string {
	w := lipgloss.Width(line)
	from, to = clamp(from, 0, w), clamp(to, 0, w)
	if from >= to {
		return line
	}
	mid := ansi.Strip(ansi.Cut(line, from, to))
	return ansi.Cut(line, 0, from) + "\x1b[7m" + mid + "\x1b[27m" + ansi.Cut(line, to, w)
}

// highlightAll reverse-videos every occurrence of query in a styled line.
func highlightAll(line, query string) string {
	plain := strings.ToLower(ansi.Strip(line))
	q := strings.ToLower(query)
	if q == "" {
		return line
	}
	var ranges [][2]int
	for i := 0; ; {
		j := strings.Index(plain[i:], q)
		if j < 0 {
			break
		}
		start := len([]rune(plain[:i+j]))
		ranges = append(ranges, [2]int{start, start + len([]rune(q))})
		i += j + len(q)
	}
	for k := len(ranges) - 1; k >= 0; k-- { // right-to-left keeps columns valid
		line = highlightRange(line, ranges[k][0], ranges[k][1])
	}
	return line
}

// anchorScroll keeps a scrolled-up view pinned to the same history lines as
// new output streams in. scrollOff counts lines up from the live bottom, so a
// busy head (e.g. a Claude session redrawing and appending) would otherwise
// slide the view toward the bottom on every new line — making it feel like you
// can't scroll back at all. Growing scrollOff by however many lines were pushed
// into scrollback holds the view still. Called every tick.
func (m *consoleModel) anchorScroll() {
	it := m.cur()
	if it == nil || it.head == nil {
		m.lastSbLen = 0
		return
	}
	sb := it.head.ScrollbackLen()
	if m.scrollOff > 0 && sb > m.lastSbLen {
		m.scrollOff += sb - m.lastSbLen
		if m.scrollOff > sb {
			m.scrollOff = sb
		}
	}
	m.lastSbLen = sb
}

// scrollBy moves the focused head's view into or out of its scrollback.
func (m *consoleModel) scrollBy(n int) {
	it := m.cur()
	if it == nil || it.head == nil {
		return
	}
	m.selActive = false // row coords go stale once the view scrolls
	m.scrollOff += n
	if m.scrollOff < 0 {
		m.scrollOff = 0
	}
	if hi := it.head.ScrollbackLen(); m.scrollOff > hi {
		m.scrollOff = hi
	}
}

func (m *consoleModel) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// F1 help and F5 refresh work in every mode.
	switch msg.String() {
	case "f1":
		m.helpOpen = !m.helpOpen
		return m, nil
	case "f5":
		m.refresh()
		m.setFlash("refreshed")
		return m, nil
	}
	if m.helpOpen { // any other key dismisses help
		if msg.Type == tea.KeyEsc || msg.String() == "q" {
			m.helpOpen = false
		}
		return m, nil
	}

	if m.searching {
		switch msg.String() {
		case "enter":
			m.runSearch()
			m.searching = false
		case "esc":
			m.searching, m.searchQuery, m.matches = false, "", nil
		case "backspace":
			if len(m.searchQuery) > 0 {
				m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.searchQuery += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.searchQuery += " "
			}
		}
		return m, nil
	}

	if m.renaming {
		switch msg.String() {
		case "enter":
			if it := m.cur(); it != nil && it.head != nil {
				it.head.Rename(m.input)
			}
			m.renaming, m.input = false, ""
			m.refresh()
		case "esc":
			m.renaming, m.input = false, ""
		case "ctrl+u":
			m.input = ""
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.input += " "
			}
		}
		return m, nil
	}

	if m.prompting {
		switch msg.String() {
		case "enter":
			m.submitPrompt()
			m.closePrompt()
			m.refresh()
		case "esc":
			m.closePrompt()
		case "ctrl+t": // cycle Claude / shell / custom
			m.promptMode, m.completes = (m.promptMode+1)%3, nil
		case "tab": // path completion (not for custom commands)
			if m.promptMode != modeCustom {
				m.input, m.completes = completeDir(m.input)
				m.promptSel = -1
			}
		case "up":
			m.pickSaved(-1)
		case "down":
			m.pickSaved(1)
		case "ctrl+u":
			m.input, m.promptSel, m.completes = "", -1, nil
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
			m.promptSel, m.completes = -1, nil
		default:
			if msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.input += " "
			}
			m.promptSel, m.completes = -1, nil
		}
		return m, nil
	}

	// Alt+Up/Down jump between heads — works while focused too, so you can
	// switch live terminals without detaching.
	switch msg.String() {
	case "alt+up":
		m.moveSelection(-1)
		return m, nil
	case "alt+down":
		m.moveSelection(1)
		return m, nil
	}

	if m.focusTerm {
		// Hydra intercepts a few keys; everything else goes to the child,
		// so Ctrl+C still interrupts Claude, Esc still works, etc.
		switch msg.String() {
		case "ctrl+q":
			m.focusTerm = false
			m.setFlash("detached — arrows to select, Enter to re-focus")
			return m, nil
		case "shift+pgup":
			m.scrollBy(m.termRows() / 2)
			return m, nil
		case "shift+pgdown":
			m.scrollBy(-m.termRows() / 2)
			return m, nil
		}
		if it := m.cur(); it != nil && it.head != nil {
			m.scrollOff = 0 // any input jumps back to the live view
			if msg.Paste {
				// Wrap pasted text so shells/Claude treat it as one block.
				it.head.Send([]byte("\x1b[200~" + string(msg.Runes) + "\x1b[201~"))
			} else if b := keyToBytes(msg); b != nil {
				it.head.Send(b)
			}
		}
		return m, nil
	}

	// Sidebar navigation mode.
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if time.Since(m.quitAt) < 2*time.Second {
			return m, tea.Quit
		}
		m.quitAt = time.Now()
		m.setFlash("press q again to quit")
	case "s":
		if it := m.cur(); it != nil && it.dir != "" {
			if core.ToggleSavedPath(it.dir) {
				m.setFlash("★ saved " + it.dir)
			} else {
				m.setFlash("removed saved path " + it.dir)
			}
			m.loadSaved()
		}
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.resetView()
		}
	case "down", "j":
		if m.selected < len(m.items)-1 {
			m.selected++
			m.resetView()
		}
	case "pgup":
		m.scrollBy(m.termRows() / 2)
	case "pgdown":
		m.scrollBy(-m.termRows() / 2)
	case "/":
		if it := m.cur(); it != nil && it.head != nil {
			m.searching, m.searchQuery = true, ""
		}
	case "n":
		m.stepMatch(-1) // older / up (reverse search direction)
	case "N":
		m.stepMatch(1) // newer / down
	case "esc":
		m.selActive, m.searchQuery, m.matches = false, "", nil
	case "enter":
		if it := m.cur(); it != nil && it.live {
			m.focusTerm = true
			m.resizeFocused()
			m.setFlash("focused — Ctrl+Q to detach")
		} else {
			m.setFlash("this head has ended — Ctrl+X to remove it")
		}
	case "ctrl+n":
		m.prompting, m.promptMode, m.promptSel, m.completes = true, modeShell, -1, nil
		m.loadSaved()
		m.input = ""
		if cwd, err := os.Getwd(); err == nil {
			m.input = cwd
		}
	case "R":
		if it := m.cur(); it != nil && it.head != nil {
			m.renaming, m.input = true, it.name
		}
	case "ctrl+x":
		if it := m.cur(); it != nil && it.head != nil {
			it.head.Close()
			m.setFlash("closed " + it.name)
			m.refresh()
		}
	case "r":
		m.refresh()
	}
	return m, nil
}

// resetView clears per-head view state when the selection changes.
func (m *consoleModel) resetView() {
	m.scrollOff = 0
	m.selActive = false
	m.searchQuery, m.matches = "", nil
	// Re-baseline the drift anchor to the newly selected head so a scroll
	// before the next tick can't be measured against the old head's history.
	if it := m.cur(); it != nil && it.head != nil {
		m.lastSbLen = it.head.ScrollbackLen()
	} else {
		m.lastSbLen = 0
	}
}

func (m *consoleModel) closePrompt() {
	m.prompting, m.input, m.promptMode, m.promptSel, m.completes = false, "", modeShell, -1, nil
}

// pickSaved cycles the highlight through the saved-path list, filling the
// input with the chosen path (-1 returns to the free-text line).
func (m *consoleModel) pickSaved(d int) {
	if len(m.savedPaths) == 0 {
		return
	}
	m.promptSel += d
	if m.promptSel < -1 {
		m.promptSel = len(m.savedPaths) - 1
	}
	if m.promptSel >= len(m.savedPaths) {
		m.promptSel = -1
	}
	if m.promptSel >= 0 {
		m.input = m.savedPaths[m.promptSel]
	}
	m.completes = nil
}

// moveSelection jumps between heads, keeping focus if focused.
func (m *consoleModel) moveSelection(d int) {
	if len(m.items) == 0 {
		return
	}
	n := clamp(m.selected+d, 0, len(m.items)-1)
	if n != m.selected {
		m.selected = n
		m.resetView()
		m.resizeFocused()
	}
}

func (m *consoleModel) cur() *item {
	if m.selected >= 0 && m.selected < len(m.items) {
		return &m.items[m.selected]
	}
	return nil
}

// submitPrompt spawns a head from the new-agent prompt, interpreting the
// input per the chosen mode.
func (m *consoleModel) submitPrompt() {
	input := strings.TrimSpace(m.input)
	switch m.promptMode {
	case modeCustom:
		if input == "" {
			m.setFlash("enter a command to run")
			return
		}
		cwd, _ := os.Getwd()
		m.spawnHead(cwd, input, strings.ToUpper(firstWord(input)))
	case modeShell:
		m.spawnHead(input, "", "")
	default: // modeClaude
		autorun := os.Getenv("HYDRA_AUTORUN")
		if autorun == "" {
			autorun = "claude"
		}
		m.spawnHead(input, autorun, "")
	}
}

func (m *consoleModel) spawnHead(dir, autorun, name string) {
	dir = expandHome(dir)
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		m.setFlash("not a directory: " + dir)
		return
	}
	if name == "" {
		name = strings.ToUpper(filepath.Base(dir))
	}
	s, err := m.mgr.Spawn(name, dir, autorun, m.termCols(), m.termRows())
	if err != nil {
		m.setFlash("spawn failed: " + err.Error())
		return
	}
	m.setFlash("spawned " + name)
	m.refresh()
	for i, it := range m.items { // select and focus the new head
		if it.id == s.ID {
			m.selected, m.focusTerm = i, true
			break
		}
	}
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func (m *consoleModel) resizeFocused() {
	if it := m.cur(); it != nil && it.head != nil {
		it.head.Resize(m.termCols(), m.termRows())
	}
}

// refresh rebuilds the sidebar. Hydra tracks only the heads it spawned —
// sessions launched in other terminals are intentionally not shown here.
func (m *consoleModel) refresh() {
	byHead := core.StatesByHead()

	var items []item
	for _, h := range m.mgr.List() {
		// "ready" = live shell head with no Claude session reporting yet.
		state, age := "ready", int(time.Since(h.Started).Seconds())
		if st := byHead[h.ID]; st != nil {
			state, age = st.State, int(time.Since(st.LastSeen).Seconds())
		}
		if !h.Alive() {
			state = "ended"
		}
		items = append(items, item{
			kind: "head", id: h.ID, name: h.DisplayName(), dir: h.Dir,
			state: state, ageSecs: age, live: h.Alive(), head: h,
		})
	}
	m.items = items
	if m.selected >= len(items) {
		m.selected = max(len(items)-1, 0)
	}
}

// ---- view ----

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	paneFocused = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("13"))
	sbHeadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Bold(true)
	selStyle    = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	dimText     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	flashStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
)

// stateBadge maps a hook state to the CloudOps-style label + dot color.
func stateBadge(state string) (label string, color lipgloss.Color) {
	switch state {
	case "working":
		return "Running", "10"
	case "needs-you":
		return "Pending Input", "214"
	case "done":
		return "Done", "14"
	case "started", "starting":
		return "Starting", "11"
	case "ready":
		return "Ready", "10"
	case "ended":
		return "Stopped", "9"
	default:
		return "Inactive", "244"
	}
}

func (m *consoleModel) View() string {
	if m.width == 0 {
		return "starting hydra…"
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.viewSidebar(), m.viewTerminal())
	return m.viewTitle() + "\n" + body + "\n" + m.viewFooter()
}

// viewTitle is the top bar: brand on the left, live head-count summary and
// clock on the right.
func (m *consoleModel) viewTitle() string {
	left := titleStyle.Render(" ☁ HYDRA ") + dimText.Render("· many heads, one brain")
	counts := map[string]int{}
	for _, it := range m.items {
		counts[it.state]++
	}
	var badges []string
	for _, st := range []string{"needs-you", "working", "ready", "done", "ended"} {
		if counts[st] > 0 {
			label, color := stateBadge(st)
			badges = append(badges, lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%d %s", counts[st], label)))
		}
	}
	right := strings.Join(badges, dimText.Render(" · "))
	if right != "" {
		right += dimText.Render("  ")
	}
	if m.newVersion != "" {
		right += lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("⬆ "+m.newVersion+" — hydra update") + dimText.Render("  ")
	}
	right += dimText.Render(time.Now().Format("15:04:05")) + " "
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *consoleModel) viewSidebar() string {
	h := m.bodyHeight()
	inner := sidebarW - 2
	saved := map[string]bool{}
	for _, p := range m.savedPaths {
		saved[p] = true
	}
	rows := []string{sbHeadStyle.Render("PROJECTS & AGENTS")}
	for i, it := range m.items {
		label, color := stateBadge(it.state)
		dot := lipgloss.NewStyle().Foreground(color).Render("●")
		name := truncate(it.name, 14)
		star := " "
		if saved[it.dir] {
			star = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("★")
		}
		meta := label + " " + humanAge(time.Duration(it.ageSecs)*time.Second)
		line := fmt.Sprintf("%s %-14s%s", dot, name, star)
		metaLine := dimText.Render(truncate(meta, inner-2))
		block := line + "\n   " + metaLine
		if i == m.selected {
			block = selStyle.Width(inner).Render(line) + "\n" + selStyle.Width(inner).Render("   "+truncate(meta, inner-3))
		}
		rows = append(rows, block)
	}
	if len(m.items) == 0 {
		rows = append(rows, dimText.Render("no sessions yet"), dimText.Render("Ctrl+N to spawn one"))
	}
	content := strings.Join(rows, "\n")
	return paneStyle.Width(inner).Height(h - 2).Render(content)
}

// viewTerminal draws the right pane as a hand-built box. The embedded
// terminal's raw ANSI is passed through untouched (only ANSI-aware
// truncate/pad is applied) so lipgloss never reprocesses foreign escape
// sequences — which is what caused stray color-parse errors otherwise.
func (m *consoleModel) viewTerminal() string {
	innerW := m.termCols()
	rows := m.termRows() + 1 // +1 header line
	borderColor := lipgloss.Color("240")
	if m.focusTerm {
		borderColor = lipgloss.Color("13")
	}

	var lines []string
	switch {
	case m.helpOpen:
		borderColor = lipgloss.Color("13")
		lines = helpLines()
	case m.renaming:
		lines = []string{
			promptStyle.Render("Rename head:"), "",
			"  " + m.input + "▏", "",
			dimText.Render("Enter to rename · Esc to cancel"),
		}
	case m.prompting:
		lines = m.promptLines()
	case m.cur() == nil:
		lines = []string{dimText.Render("No session selected."), dimText.Render("Ctrl+N to spawn a live head.")}
	default:
		it := m.cur()
		header := titleStyle.Render("["+strings.ToLower(it.name)+" ") + dimText.Render(it.dir+"]")
		switch {
		case m.searchQuery != "" && len(m.matches) > 0:
			header += "  " + flashStyle.Render(fmt.Sprintf("/%s  %d/%d", m.searchQuery, m.matchIdx+1, len(m.matches)))
		case m.scrollOff > 0:
			header += "  " + flashStyle.Render(fmt.Sprintf("⟲ SCROLLBACK −%d", m.scrollOff))
		}
		lines = append(lines, header)

		cx, cy := -1, -1
		if m.focusTerm && m.scrollOff == 0 { // cursor only meaningful on the live screen
			cx, cy = it.head.CursorPos()
		}
		for r, line := range m.visibleLines() {
			col := -1
			if r == cy {
				col = cx
			}
			lines = append(lines, m.decorate(line, r, col))
		}
	}
	return boxify(lines, innerW, rows, borderColor)
}

// promptLines renders the new-head prompt: the path/command input, Tab
// completion candidates, and the saved-paths quick-pick list.
func (m *consoleModel) promptLines() []string {
	label := "working directory:"
	if m.promptMode == modeCustom {
		label = "command to run (e.g. ssh user@host):"
	}
	cursor := ""
	if m.promptSel < 0 {
		cursor = "▏"
	}
	out := []string{
		promptStyle.Render("New "+modeName[m.promptMode]+" head") + dimText.Render("  (Ctrl+T changes type)"),
		"",
		"  " + m.input + cursor,
	}
	if len(m.completes) > 0 { // Tab candidates, like a shell
		out = append(out, dimText.Render("  ↳ "+strings.Join(m.completes, "  ")))
	}
	if m.promptMode != modeCustom && len(m.savedPaths) > 0 {
		out = append(out, "", dimText.Render("Saved paths ")+dimText.Render("(↑/↓ to pick):"))
		for i, p := range m.savedPaths {
			row := "  ★ " + p
			if i == m.promptSel {
				row = selStyle.Render(" ★ " + p + " ")
			}
			out = append(out, row)
		}
	}
	out = append(out, "", dimText.Render(label+"  Tab complete · Enter spawn · Esc cancel"))
	return out
}

// boxify frames content in a rounded box of exactly innerW×rows, keeping
// embedded ANSI intact via ansi-aware width handling.
func boxify(lines []string, innerW, rows int, border lipgloss.Color) string {
	bs := lipgloss.NewStyle().Foreground(border)
	bar := bs.Render("│")
	var b strings.Builder
	b.WriteString(bs.Render("╭"+strings.Repeat("─", innerW)+"╮") + "\n")
	for i := 0; i < rows; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		line = ansiTrunc(line, innerW)
		if pad := innerW - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		b.WriteString(bar + line + "\x1b[0m" + bar + "\n")
	}
	b.WriteString(bs.Render("╰" + strings.Repeat("─", innerW) + "╯"))
	return b.String()
}

func (m *consoleModel) viewFooter() string {
	if m.searching {
		return promptStyle.Render(" /") + m.searchQuery + "▏" + dimText.Render("   Enter search · Esc cancel")
	}
	if m.flash != "" && time.Now().Before(m.flashUntil) {
		return flashStyle.Render(" » " + m.flash)
	}
	info := "Info: "
	if it := m.cur(); it != nil {
		label, _ := stateBadge(it.state)
		info += it.name + " · " + label
	} else {
		info += "no session"
	}
	var keys string
	switch {
	case m.helpOpen:
		keys = "F1/Esc Close help"
	case m.focusTerm:
		keys = "Ctrl+Q Detach · Alt+↑/↓ Switch head · wheel Scroll · F1 Help · (keys → terminal)"
	default:
		keys = "↑/↓ Select · Alt+↑/↓ Jump · Enter Focus · s Save · Ctrl+N New · / Search · F1 · qq Quit"
	}
	gap := m.width - lipgloss.Width(info) - lipgloss.Width(keys) - 2
	if gap < 1 {
		gap = 1
	}
	return footerStyle.Render(" "+info) + strings.Repeat(" ", gap) + footerStyle.Render(keys)
}

// helpLines is the F1 overlay content.
func helpLines() []string {
	title := titleStyle.Render
	dim := dimText.Render
	return []string{
		title("HYDRA — keybindings"), "",
		dim("Sidebar (detached)"),
		"  ↑/↓ or j/k     select a head    Alt+↑/↓  jump between heads",
		"  Enter          focus its terminal (type into it)",
		"  s              save / unsave this head's path (★)",
		"  Ctrl+N         new head — Tab completes paths, ↑/↓ picks saved,",
		"                 Ctrl+T switches Claude / shell / custom",
		"  R  rename · Ctrl+X close · PgUp/PgDn scroll · drag to copy",
		"  /              search history · n older · N newer · Esc clear",
		"  q q            quit hydra (press twice) · Ctrl+C quits now",
		"",
		dim("Focused terminal"),
		"  Ctrl+Q  detach     Alt+↑/↓  switch head without detaching",
		"  Wheel / Shift+PgUp/PgDn   scroll history",
		"  Ctrl+←/→ word move · Ctrl+Backspace word delete · Ctrl+R shell search",
		"  all other keys go straight to Claude / the shell",
		"",
		dim("Anywhere:  F1 help · F5 refresh"),
	}
}
