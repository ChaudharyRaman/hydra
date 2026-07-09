package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"hydra/internal/terminal"
)

// waitForScrollback blocks until the head has at least n scrollback lines or
// the deadline passes, so the test doesn't race the emulator's read loop.
func waitForScrollback(h *terminal.Session, n int) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.ScrollbackLen() >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAnchorScrollHoldsViewWhileStreaming reproduces the "can't scroll back
// while a Claude session is running" bug: scrollOff is measured from the live
// bottom, so streaming output slides a scrolled-up view toward the tail.
// anchorScroll must grow scrollOff to keep the same history lines in view.
func TestAnchorScrollHoldsViewWhileStreaming(t *testing.T) {
	h, err := terminal.New("test", ".", "", 40, 6)
	if err != nil {
		t.Fatalf("spawn head: %v", err)
	}
	defer h.Close()

	emit := func(from, to int) {
		var b strings.Builder
		for i := from; i < to; i++ {
			b.WriteString(fmt.Sprintf("printf 'LINE%03d\\n'\n", i))
		}
		h.Send([]byte(b.String()))
	}

	emit(0, 60)
	waitForScrollback(h, 40)

	m := &consoleModel{
		mgr:      terminal.NewManager(),
		items:    []item{{kind: "head", head: h, live: true}},
		selected: 0,
		height:   11, // termRows() = height-5 = 6
	}
	m.lastSbLen = h.ScrollbackLen()

	// Scroll up into history and record the top visible line.
	m.scrollBy(20)
	topBefore := firstNonBlank(m.visibleLines())
	if topBefore == "" {
		t.Fatalf("no visible content after scrolling up")
	}

	// More output streams in, exactly the situation where the view used to
	// drift back toward the live tail.
	emit(60, 100)
	waitForScrollback(h, 80)
	m.anchorScroll()

	topAfter := firstNonBlank(m.visibleLines())
	if topAfter != topBefore {
		t.Fatalf("view drifted while streaming: top was %q, now %q", topBefore, topAfter)
	}
}

func firstNonBlank(lines []string) string {
	for _, l := range lines {
		if s := strings.TrimSpace(l); s != "" {
			return s
		}
	}
	return ""
}
