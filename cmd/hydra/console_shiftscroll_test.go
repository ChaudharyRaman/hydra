package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"hydra/internal/terminal"
)

// TestShiftArrowScroll verifies that Shift+Up/Down move the scrollback view in
// both detached and focused modes — the Mac-friendly binding added in v0.3.3.
func TestShiftArrowScroll(t *testing.T) {
	h, err := terminal.New("test", ".", "", 40, 6)
	if err != nil {
		t.Fatalf("spawn head: %v", err)
	}
	defer h.Close()

	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString(fmt.Sprintf("printf 'LINE%03d\\n'\n", i))
	}
	h.Send([]byte(b.String()))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && h.ScrollbackLen() < 40 {
		time.Sleep(20 * time.Millisecond)
	}
	if h.ScrollbackLen() < 40 {
		t.Fatalf("not enough scrollback: %d", h.ScrollbackLen())
	}

	m := &consoleModel{
		mgr:      terminal.NewManager(),
		items:    []item{{kind: "head", head: h, live: true}},
		selected: 0,
		height:   11,
	}

	up := tea.KeyMsg{Type: tea.KeyShiftUp}
	down := tea.KeyMsg{Type: tea.KeyShiftDown}
	if up.String() != "shift+up" || down.String() != "shift+down" {
		t.Fatalf("unexpected key strings: %q %q", up.String(), down.String())
	}

	check := func(mode string, focused bool) {
		m.focusTerm = focused
		m.scrollOff = 0
		m.lastSbLen = h.ScrollbackLen()

		m.onKey(up)
		if m.scrollOff != 3 {
			t.Fatalf("%s: shift+up did not scroll up (scrollOff=%d, want 3)", mode, m.scrollOff)
		}
		m.onKey(up)
		if m.scrollOff != 6 {
			t.Fatalf("%s: second shift+up (scrollOff=%d, want 6)", mode, m.scrollOff)
		}
		m.onKey(down)
		if m.scrollOff != 3 {
			t.Fatalf("%s: shift+down did not scroll back down (scrollOff=%d, want 3)", mode, m.scrollOff)
		}
	}

	check("detached", false)
	check("focused", true)
}
