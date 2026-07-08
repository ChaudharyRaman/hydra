// Hydra — many heads, one brain.
// Phase 0: hook receiver, event log, notifications, fleet status.
package main

import (
	"fmt"
	"os"
)

const usage = `hydra — mission control for concurrent Claude Code sessions

Usage:
  hydra             open the console: sidebar of sessions + the live,
                    typeable terminal of the selected head (the main UI)
  hydra dash        lightweight fleet dashboard (TUI): every session, its
                    state, and a live tail of the selected transcript.
                    enter = jump to session, y/n = answer permission prompt,
                    p = live screen preview (tmux sessions only)
  hydra new [dir]   spawn a controllable Claude head in the hydra tmux
                    session (default dir: current directory)
  hydra serve       start hydrad: web dashboard + phone approvals + task
                    queue workers (-addr 0.0.0.0:7717 for phone, -jobs N)
  hydra run "..."   queue a headless task on the daemon (-d dir, -m mode)
  hydra tasks       list queued/running/finished headless tasks
  hydra update      update hydra to the latest release
  hydra version     print the installed version
  hydra hook        read a Claude Code hook event from stdin, log it, notify if needed
                    (wired into ~/.claude/settings.json by 'hydra install')
  hydra status      show every known session and its state
  hydra status -a   include ended sessions
  hydra tail        follow the event stream live
  hydra install     add hydra hooks to ~/.claude/settings.json (backs up first)
  hydra uninstall   remove hydra hooks from ~/.claude/settings.json
`

func main() {
	if len(os.Args) < 2 {
		runConsole()
		return
	}
	switch os.Args[1] {
	case "console", "c":
		runConsole()
	case "dash":
		runDash()
	case "new":
		runNew(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "run":
		runRun(os.Args[2:])
	case "tasks":
		runTasks()
	case "update":
		runUpdate()
	case "version", "--version", "-v":
		fmt.Println("hydra", version)
	case "hook":
		runHook() // never returns non-zero: must not disturb Claude sessions
	case "status":
		all := len(os.Args) > 2 && (os.Args[2] == "-a" || os.Args[2] == "--all")
		runStatus(all)
	case "tail":
		runTail()
	case "install":
		runInstall()
	case "uninstall":
		runUninstall()
	default:
		fmt.Print(usage)
		os.Exit(1)
	}
}
