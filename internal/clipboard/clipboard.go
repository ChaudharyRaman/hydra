// Package clipboard copies text to the system clipboard through whatever
// path works: an OSC 52 escape (understood by iTerm2, most modern terminals,
// and forwarded over SSH) plus the platform's native clipboard tool.
package clipboard

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

// Copy places text on the clipboard. Best-effort: it tries both transports
// and never errors, so a missing tool just means one path is skipped.
func Copy(text string) {
	if text == "" {
		return
	}
	// OSC 52 goes to stderr (the tty) — it's out-of-band and doesn't disturb
	// the Bubble Tea renderer writing to stdout. Wrap for tmux when inside it.
	seq := osc52.New(text)
	if os.Getenv("TMUX") != "" {
		seq = seq.Tmux()
	}
	fmt.Fprint(os.Stderr, seq.String())

	if cmd := nativeCmd(); cmd != nil {
		cmd.Stdin = strings.NewReader(text)
		cmd.Run()
	}
}

func nativeCmd() *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("pbcopy")
	case "windows":
		return exec.Command("clip")
	default: // linux, bsd
		if os.Getenv("WAYLAND_DISPLAY") != "" && have("wl-copy") {
			return exec.Command("wl-copy")
		}
		if have("xclip") {
			return exec.Command("xclip", "-selection", "clipboard")
		}
		if have("xsel") {
			return exec.Command("xsel", "--clipboard", "--input")
		}
	}
	return nil
}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
