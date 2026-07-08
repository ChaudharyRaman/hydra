package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"hydra/internal/core"
	"hydra/internal/terminal"
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
	left := ansi.Cut(line, 0, col)
	ch := " "
	if col < width {
		if c := ansi.Strip(ansi.Cut(line, col, col+1)); c != "" {
			ch = c
		}
	}
	right := ""
	if col+1 < width {
		right = ansi.Cut(line, col+1, width)
	}
	return left + "\x1b[0m\x1b[7m" + ch + "\x1b[0m" + right
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
	promptMode int // modeClaude | modeShell | modeCustom

	scrollOff int  // lines scrolled up into the focused head's history (0 = live)
	helpOpen  bool // F1 help overlay

	flash      string
	flashUntil time.Time
	width      int
	height     int
	ticks      int
}

func runConsole() {
	m := &consoleModel{mgr: terminal.NewManager()}
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

func (m *consoleModel) Init() tea.Cmd { return consoleTickCmd() }

func consoleTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return consoleTick(t) })
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
		m.resizeFocused()
		return m, consoleTickCmd()
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scrollBy(3)
		case tea.MouseButtonWheelDown:
			m.scrollBy(-3)
		}
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// scrollBy moves the focused head's view into or out of its scrollback.
func (m *consoleModel) scrollBy(n int) {
	it := m.cur()
	if it == nil || it.head == nil {
		return
	}
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

	if m.prompting || m.renaming {
		switch msg.String() {
		case "enter":
			if m.renaming {
				if it := m.cur(); it != nil && it.head != nil {
					it.head.Rename(m.input)
				}
			} else {
				m.submitPrompt()
			}
			m.prompting, m.renaming, m.input, m.promptMode = false, false, "", modeClaude
			m.refresh()
		case "esc":
			m.prompting, m.renaming, m.input, m.promptMode = false, false, "", modeClaude
		case "tab":
			if m.prompting {
				m.promptMode = (m.promptMode + 1) % 3
			}
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
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.scrollOff = 0
		}
	case "down", "j":
		if m.selected < len(m.items)-1 {
			m.selected++
			m.scrollOff = 0
		}
	case "pgup":
		m.scrollBy(m.termRows() / 2)
	case "pgdown":
		m.scrollBy(-m.termRows() / 2)
	case "enter":
		if it := m.cur(); it != nil && it.live {
			m.focusTerm = true
			m.resizeFocused()
			m.setFlash("focused — Ctrl+Q to detach")
		} else {
			m.setFlash("this head has ended — Ctrl+X to remove it")
		}
	case "ctrl+n":
		m.prompting, m.promptMode = true, modeClaude
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
	rows := []string{sbHeadStyle.Render("PROJECTS & AGENTS")}
	for i, it := range m.items {
		label, color := stateBadge(it.state)
		dot := lipgloss.NewStyle().Foreground(color).Render("●")
		name := truncate(it.name, 15)
		meta := label + " " + humanAge(time.Duration(it.ageSecs)*time.Second)
		line := fmt.Sprintf("%s %-15s", dot, name)
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
		label := "working directory:"
		if m.promptMode == modeCustom {
			label = "command to run (e.g. ssh user@host):"
		}
		lines = []string{
			promptStyle.Render("New " + modeName[m.promptMode] + " head — " + label), "",
			"  " + m.input + "▏", "",
			"Runs: " + titleStyle.Render(modeName[m.promptMode]),
			dimText.Render("Enter spawn · Tab cycle Claude/shell/custom · Esc cancel"),
		}
	case m.cur() == nil:
		lines = []string{dimText.Render("No session selected."), dimText.Render("Ctrl+N to spawn a live head.")}
	default:
		it := m.cur()
		header := titleStyle.Render("["+strings.ToLower(it.name)+" ") + dimText.Render(it.dir+"]")
		if m.scrollOff > 0 {
			header += "  " + flashStyle.Render(fmt.Sprintf("⟲ SCROLLBACK −%d (type or wheel-down to return)", m.scrollOff))
		}
		lines = append(lines, header)
		if m.scrollOff > 0 {
			lines = append(lines, it.head.ViewLines(m.scrollOff, m.termRows())...)
		} else {
			emLines := strings.Split(it.head.Render(), "\n")
			if m.focusTerm { // draw the terminal cursor as a reverse-video block
				cx, cy := it.head.CursorPos()
				if cy >= 0 && cy < len(emLines) {
					emLines[cy] = cursorOverlay(emLines[cy], cx)
				}
			}
			lines = append(lines, emLines...)
		}
	}
	return boxify(lines, innerW, rows, borderColor)
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
		keys = "Ctrl+Q Detach · wheel/Shift+PgUp Scroll · F1 Help · (keys → terminal)"
	default:
		keys = "↑/↓ Select · Enter Focus · Ctrl+N New · R Rename · Ctrl+X Close · F1 Help · q Quit"
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
		"  ↑/↓ or j/k     select a head",
		"  Enter          focus its terminal (type into it)",
		"  Ctrl+N         new head (Tab in prompt: Claude / shell / custom)",
		"  R              rename the selected head",
		"  Ctrl+X         close the selected head",
		"  PgUp/PgDn      scroll selected head's output",
		"  q / Ctrl+C     quit hydra",
		"",
		dim("Focused terminal"),
		"  Ctrl+Q         detach back to the sidebar",
		"  Wheel / Shift+PgUp/PgDn   scroll history",
		"  Ctrl+←/→       move by word     Ctrl+Backspace  delete word",
		"  all other keys go straight to Claude / the shell",
		"",
		dim("Anywhere:  F1 help · F5 refresh"),
	}
}
