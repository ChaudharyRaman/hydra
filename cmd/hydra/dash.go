package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hydra/internal/core"
	"hydra/internal/tmux"
)

const (
	refreshEvery   = time.Second
	backfillWindow = 24 * time.Hour
	tailBytes      = 256 << 10 // how much transcript to parse for the detail pane
	fleetPaneWidth = 46
)

var (
	dashStateStyle = map[string]lipgloss.Style{
		"needs-you": lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true),
		"working":   lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"done":      lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		"started":   lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"idle":      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		"ended":     lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
	dashKindStyle = map[string]lipgloss.Style{
		"you":    lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true),
		"claude": lipgloss.NewStyle().Foreground(lipgloss.Color("15")),
		"tool":   lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"result": lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		"info":   lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
	}
	dashKindLabel = map[string]string{
		"you": "you   ", "claude": "claude", "tool": "⚙ tool", "result": "  ↳   ", "info": "      ",
	}
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	paneBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
)

type tickMsg time.Time

type dashModel struct {
	fleet      []core.Session
	transcript []core.Line
	selected   int // index into fleet
	scrollUp   int // detail pane lines scrolled up from the bottom (0 = live tail)
	showEnded  bool
	preview    bool // detail pane shows live tmux screen instead of transcript
	flash      string
	flashUntil time.Time
	width      int
	height     int
	err        error
}

func (m *dashModel) setFlash(msg string) {
	m.flash = msg
	m.flashUntil = time.Now().Add(4 * time.Second)
}

func runDash() {
	m := dashModel{}
	m.reload()
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
}

func (m *dashModel) reload() {
	selectedID := m.selectedID()
	fleet, err := core.FleetList(m.showEnded, backfillWindow)
	if err != nil {
		m.err = err
		return
	}
	m.err = nil
	m.fleet = fleet
	// Keep the cursor on the same session even as rows re-sort under it.
	m.selected = 0
	for i, s := range fleet {
		if s.ID == selectedID {
			m.selected = i
			break
		}
	}
	m.reloadTranscript()
}

func (m *dashModel) reloadTranscript() {
	m.transcript = nil
	if m.selected < len(m.fleet) && m.fleet[m.selected].Transcript != "" {
		m.transcript = core.Tail(m.fleet[m.selected].Transcript, tailBytes)
	}
}

func (m dashModel) selectedID() string {
	if m.selected < len(m.fleet) {
		return m.fleet[m.selected].ID
	}
	return ""
}

func (m dashModel) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.reload()
		return m, tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				m.scrollUp = 0
				m.reloadTranscript()
			}
		case "down", "j":
			if m.selected < len(m.fleet)-1 {
				m.selected++
				m.scrollUp = 0
				m.reloadTranscript()
			}
		case "g":
			m.selected, m.scrollUp = 0, 0
			m.reloadTranscript()
		case "G":
			if n := len(m.fleet); n > 0 {
				m.selected, m.scrollUp = n-1, 0
				m.reloadTranscript()
			}
		case "pgup", "u":
			m.scrollUp += m.detailHeight() / 2
			if max := len(m.transcript) - 1; m.scrollUp > max {
				m.scrollUp = max
			}
			if m.scrollUp < 0 {
				m.scrollUp = 0
			}
		case "pgdown", "d":
			m.scrollUp -= m.detailHeight() / 2
			if m.scrollUp < 0 {
				m.scrollUp = 0
			}
		case "a":
			m.showEnded = !m.showEnded
			m.reload()
		case "r":
			m.reload()
		case "p":
			m.preview = !m.preview
		case "enter":
			return m.jump()
		case "y":
			m.answer(true)
		case "n":
			m.answer(false)
		}
	}
	return m, nil
}

// jump moves the user into the selected session's tmux pane: switch-client
// when the dashboard itself runs inside tmux, a nested attach otherwise.
func (m dashModel) jump() (tea.Model, tea.Cmd) {
	if m.selected >= len(m.fleet) {
		return m, nil
	}
	s := m.fleet[m.selected]
	if s.TmuxPane == "" {
		m.setFlash("session not in tmux — spawn controllable heads with 'hydra new <dir>'")
		return m, nil
	}
	if !tmux.PaneExists(s.TmuxPane) {
		m.setFlash("tmux pane " + s.TmuxPane + " is gone")
		return m, nil
	}
	if tmux.Inside() {
		if err := tmux.SwitchTo(s.TmuxPane); err != nil {
			m.setFlash("switch failed: " + err.Error())
		}
		return m, nil
	}
	cmd := tmux.AttachCmd(s.TmuxPane)
	return m, tea.ExecProcess(cmd, func(error) tea.Msg { return tickMsg(time.Now()) })
}

// answer approves (y → "1") or rejects (n → Escape) a pending permission
// prompt — but only after verifying the dialog is actually on the pane's
// screen, so we never type into a shell or an unrelated menu.
func (m *dashModel) answer(approve bool) {
	if m.selected >= len(m.fleet) {
		return
	}
	s := m.fleet[m.selected]
	project := filepath.Base(s.CWD)
	if s.TmuxPane == "" {
		m.setFlash("session not in tmux — cannot answer remotely")
		return
	}
	if !tmux.PaneExists(s.TmuxPane) {
		m.setFlash("tmux pane " + s.TmuxPane + " is gone")
		return
	}
	if !tmux.PermissionVisible(s.TmuxPane) {
		m.setFlash("no permission dialog on " + project + "'s screen — nothing sent")
		return
	}
	key, verb := "1", "approved"
	if !approve {
		key, verb = "Escape", "rejected"
	}
	if err := tmux.SendKeys(s.TmuxPane, key); err != nil {
		m.setFlash("send failed: " + err.Error())
		return
	}
	m.setFlash(verb + " pending action in " + project)
	m.reload()
}

func (m dashModel) detailHeight() int { return max(m.height-5, 3) }

func (m dashModel) View() string {
	if m.width == 0 {
		return "loading…"
	}
	header := m.viewHeader()
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.viewFleet(), m.viewDetail())
	footer := dimStyle.Render(" ↑/↓ select · enter jump · y/n answer prompt · p screen/transcript · a ended · q quit")
	if m.flash != "" && time.Now().Before(m.flashUntil) {
		footer = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render(" » " + m.flash)
	}
	return header + "\n" + body + "\n" + footer
}

func (m dashModel) viewHeader() string {
	counts := map[string]int{}
	for _, s := range m.fleet {
		counts[s.State]++
	}
	parts := []string{}
	for _, st := range []string{"needs-you", "working", "done", "started", "idle", "ended"} {
		if counts[st] > 0 {
			parts = append(parts, dashStateStyle[st].Render(fmt.Sprintf("%d %s", counts[st], st)))
		}
	}
	summary := "no sessions yet — start a claude session anywhere"
	if len(parts) > 0 {
		summary = strings.Join(parts, dimStyle.Render(" · "))
	}
	return headerStyle.Render(" HYDRA ") + dimStyle.Render("· many heads, one brain   ") + summary
}

func (m dashModel) viewFleet() string {
	h := m.detailHeight()
	var rows []string
	if m.err != nil {
		rows = append(rows, "error: "+m.err.Error())
	}
	// Window the list around the cursor so big fleets still fit.
	start := 0
	if m.selected >= h {
		start = m.selected - h + 1
	}
	for i := start; i < len(m.fleet) && len(rows) < h; i++ {
		s := m.fleet[i]
		project := filepath.Base(s.CWD)
		if project == "." || project == "" {
			project = "?"
		}
		mark := " "
		if !s.Tracked {
			mark = "°" // backfilled: mtime-based, not live
		} else if s.TmuxPane != "" {
			mark = "▸" // in tmux: hydra can jump to it and answer prompts
		}
		row := fmt.Sprintf("%s%-9s %-16s %5s %s",
			mark, s.State, truncate(project, 16), humanAge(time.Since(s.LastSeen)), short(s.ID))
		row = truncate(row, fleetPaneWidth-4)
		if i == m.selected {
			rows = append(rows, selectedStyle.Render(padTo(row, fleetPaneWidth-4)))
		} else {
			rows = append(rows, dashStateStyle[s.State].Render(row))
		}
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return paneBorder.Width(fleetPaneWidth - 2).Render(strings.Join(rows, "\n"))
}

func (m dashModel) viewDetail() string {
	h := m.detailHeight()
	w := max(m.width-fleetPaneWidth-2, 20)
	var rows []string

	if m.selected < len(m.fleet) {
		s := m.fleet[m.selected]
		title := fmt.Sprintf("%s · %s · %s", filepath.Base(s.CWD), short(s.ID), s.State)
		if s.Detail != "" {
			title += " — " + s.Detail
		}
		if m.preview && s.TmuxPane != "" {
			title = "[live screen] " + title
		}
		rows = append(rows, headerStyle.Render(truncate(title, w-2)), "")

		// Preview mode: render the pane's actual screen — exactly what you'd
		// see attached, including any pending permission dialog.
		if m.preview && s.TmuxPane != "" && tmux.PaneExists(s.TmuxPane) {
			screen, err := tmux.Capture(s.TmuxPane)
			if err == nil {
				for _, ln := range strings.Split(strings.TrimRight(screen, "\n"), "\n") {
					rows = append(rows, truncate(ln, w-4))
				}
				for len(rows) < h {
					rows = append(rows, "")
				}
				return paneBorder.Width(w).Render(strings.Join(rows[:h], "\n"))
			}
		}
	}

	lines := m.transcript
	visible := h - len(rows)
	end := len(lines) - m.scrollUp
	if end < 0 {
		end = 0
	}
	start := max(end-visible, 0)
	for _, ln := range lines[start:end] {
		style, label := dashKindStyle[ln.Kind], dashKindLabel[ln.Kind]
		rows = append(rows, style.Render(label+" ")+truncate(ln.Text, w-10))
	}
	if len(lines) == 0 {
		rows = append(rows, dimStyle.Render("no transcript yet"))
	}
	if m.scrollUp > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("… %d lines below (PgDn) …", m.scrollUp)))
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return paneBorder.Width(w).Render(strings.Join(rows[:h], "\n"))
}

func padTo(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
