// Package tmux is hydra's control plane: spawning Claude heads as tmux
// windows, jumping to them, reading their screens, and answering prompts.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HydraSession is the tmux session that 'hydra new' spawns heads into.
const HydraSession = "hydra"

func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// Inside reports whether the current process runs inside a tmux client.
func Inside() bool { return os.Getenv("TMUX") != "" }

func PaneExists(pane string) bool {
	return pane != "" && exec.Command("tmux", "display-message", "-p", "-t", pane, "").Run() == nil
}

// Capture returns the visible screen content of a pane.
func Capture(pane string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	return string(out), err
}

// SendKeys types into a pane. Keys use tmux syntax: "1", "Escape", "Enter".
func SendKeys(pane string, keys ...string) error {
	args := append([]string{"send-keys", "-t", pane}, keys...)
	return exec.Command("tmux", args...).Run()
}

// SwitchTo moves the current tmux client to the window holding pane.
// Only valid when running inside tmux.
func SwitchTo(pane string) error {
	if err := exec.Command("tmux", "select-window", "-t", pane).Run(); err != nil {
		return err
	}
	exec.Command("tmux", "select-pane", "-t", pane).Run()
	return exec.Command("tmux", "switch-client", "-t", pane).Run()
}

// AttachCmd builds the command that attaches a non-tmux terminal to the
// window holding pane. The caller wires it to the real stdin/stdout.
func AttachCmd(pane string) *exec.Cmd {
	exec.Command("tmux", "select-window", "-t", pane).Run()
	exec.Command("tmux", "select-pane", "-t", pane).Run()
	return exec.Command("tmux", "attach-session", "-t", pane)
}

// SpawnWindow starts command in a new window of the hydra session
// (creating the session on first use) and returns the new pane's %id.
func SpawnWindow(dir, name, command string) (string, error) {
	var out []byte
	var err error
	if exec.Command("tmux", "has-session", "-t", "="+HydraSession).Run() != nil {
		out, err = exec.Command("tmux", "new-session", "-d", "-P", "-F", "#{pane_id}",
			"-s", HydraSession, "-n", name, "-c", dir, command).Output()
	} else {
		out, err = exec.Command("tmux", "new-window", "-d", "-P", "-F", "#{pane_id}",
			"-t", HydraSession+":", "-n", name, "-c", dir, command).Output()
	}
	if err != nil {
		return "", fmt.Errorf("tmux spawn failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// permissionMarkers identify Claude Code's approval dialogs on screen.
var permissionMarkers = []string{"Do you want", "Would you like", "1. Yes"}

// PermissionVisible checks the pane's screen for a pending approval dialog,
// so hydra never types an answer into something else.
func PermissionVisible(pane string) bool {
	screen, err := Capture(pane)
	if err != nil {
		return false
	}
	for _, marker := range permissionMarkers {
		if strings.Contains(screen, marker) {
			return true
		}
	}
	return false
}
