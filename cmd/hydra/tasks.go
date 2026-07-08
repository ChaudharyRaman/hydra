package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"hydra/internal/task"
)

var taskColor = map[string]string{
	"failed": "\033[1;31m", "running": "\033[32m", "queued": "\033[33m", "done": "\033[36m",
}

func runTasks() {
	store, err := task.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	list := store.List()
	if len(list) == 0 {
		fmt.Println("No tasks yet. Queue one: hydra run -d <dir> \"the prompt\"")
		return
	}
	color := isTTY()
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tPROJECT\tAGE\tCOST\tPROMPT\tRESULT")
	for _, t := range list {
		status := t.Status
		if color {
			status = taskColor[t.Status] + t.Status + "\033[0m"
		}
		cost := ""
		if t.CostUSD > 0 {
			cost = fmt.Sprintf("$%.3f", t.CostUSD)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			status, filepath.Base(t.Dir), humanAge(time.Since(t.CreatedAt)), cost,
			truncate(t.Prompt, 40), truncate(t.Result, 40))
	}
	w.Flush()
}
