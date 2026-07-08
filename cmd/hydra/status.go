package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"hydra/internal/core"
)

var stateColor = map[string]string{
	"needs-you": "\033[1;31m", // bold red
	"working":   "\033[32m",   // green
	"done":      "\033[36m",   // cyan
	"started":   "\033[33m",   // yellow
	"idle":      "\033[90m",   // grey
	"ended":     "\033[90m",
}

func runStatus(includeEnded bool) {
	list, err := core.FleetList(includeEnded, 24*time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hydra: %v\n", err)
		os.Exit(1)
	}

	color := isTTY()
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATE\tPROJECT\tAGE\tSESSION\tLAST")
	for _, s := range list {
		state := s.State
		if color {
			state = stateColor[s.State] + s.State + "\033[0m"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			state, filepath.Base(s.CWD), humanAge(time.Since(s.LastSeen)), short(s.ID), truncate(s.Detail, 60))
	}
	w.Flush()
	if len(list) == 0 {
		fmt.Println("No live sessions. Run 'hydra install', then start or restart a Claude session.")
	}
}

func runTail() {
	f, err := os.Open(core.EventsPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra: no event log yet — run 'hydra install' and start a Claude session")
		os.Exit(1)
	}
	defer f.Close()
	f.Seek(0, io.SeekEnd)
	fmt.Println("Following", core.EventsPath(), "(Ctrl-C to stop)")

	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if err != nil {
			return
		}
		var ev core.Event
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		detail := ev.Message
		if detail == "" {
			detail = ev.Tool
		}
		fmt.Printf("%s  %-16s %-24s %s\n",
			ev.TS.Format("15:04:05"), ev.Event, filepath.Base(ev.CWD)+" ("+short(ev.SessionID)+")", detail)
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func isTTY() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
