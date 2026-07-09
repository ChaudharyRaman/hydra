package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// TestFixHyperlinksSwapsFields guards against the upstream vt bug that parses
// and re-emits OSC 8 hyperlinks with the params and uri fields swapped, which
// made clicking a link in the outer terminal try to open the params string
// (e.g. "id=1mgdcgr") instead of the real URL.
func TestFixHyperlinksSwapsFields(t *testing.T) {
	e := vt.NewSafeEmulator(60, 3)
	// Proper OSC 8: ESC ]8; params ; uri BEL  text  ESC ]8;; BEL
	e.Write([]byte("\x1b]8;id=1mgdcgr;https://google.com\x07google\x1b]8;;\x07"))

	raw := e.Render()
	if !strings.Contains(raw, "\x1b]8;https://google.com;id=1mgdcgr\x07") {
		t.Fatalf("expected upstream to emit swapped fields, got %q", raw)
	}

	fixed := fixHyperlinks(raw)
	if !strings.Contains(fixed, "\x1b]8;id=1mgdcgr;https://google.com\x07") {
		t.Fatalf("params;uri not restored, got %q", fixed)
	}
	if strings.Contains(fixed, "\x1b]8;https://google.com;id=1mgdcgr\x07") {
		t.Fatalf("swapped sequence still present, got %q", fixed)
	}
}

func TestFixHyperlinksNoLinks(t *testing.T) {
	in := "plain \x1b[1mbold\x1b[0m text"
	if got := fixHyperlinks(in); got != in {
		t.Fatalf("mutated link-free output: %q", got)
	}
}
