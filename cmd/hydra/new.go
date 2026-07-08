package main

import (
	"fmt"
	"os"
	"path/filepath"

	"hydra/internal/tmux"
)

// runNew spawns a Claude Code head in a tmux window hydra can control.
// HYDRA_CLAUDE_CMD overrides the launched command (wrappers, testing).
func runNew(args []string) {
	if !tmux.Available() {
		fmt.Fprintln(os.Stderr, "hydra: tmux not found — 'hydra new' needs tmux for controllable heads")
		os.Exit(1)
	}
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "hydra: %s is not a directory\n", abs)
		os.Exit(1)
	}

	command := os.Getenv("HYDRA_CLAUDE_CMD")
	if command == "" {
		command = "claude"
	}
	pane, err := tmux.SpawnWindow(abs, filepath.Base(abs), command)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	fmt.Printf("Spawned head %q in tmux session %q (pane %s)\n", filepath.Base(abs), tmux.HydraSession, pane)

	if tmux.Inside() {
		tmux.SwitchTo(pane)
		return
	}
	if isTTY() {
		cmd := tmux.AttachCmd(pane)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Run()
		return
	}
	fmt.Printf("Attach with: tmux attach -t %s\n", tmux.HydraSession)
}
