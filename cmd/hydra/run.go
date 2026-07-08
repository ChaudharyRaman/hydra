package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hydra/internal/core"
)

// runRun enqueues a headless task on the daemon.
func runRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dir := fs.String("d", "", "working directory for the task (default: current directory)")
	mode := fs.String("m", "acceptEdits", "permission mode: default|acceptEdits|plan|bypassPermissions")
	fs.Parse(args)
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, `usage: hydra run [-d dir] [-m mode] "the task prompt"`)
		os.Exit(1)
	}
	if *dir == "" {
		*dir, _ = os.Getwd()
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}

	token, err := os.ReadFile(filepath.Join(core.HydraDir(), "token"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra: no daemon token found — is 'hydra serve' running?")
		os.Exit(1)
	}
	addr := os.Getenv("HYDRA_ADDR")
	if addr == "" {
		addr = "127.0.0.1:7717"
	}

	body, _ := json.Marshal(map[string]string{"prompt": prompt, "dir": abs, "permission_mode": *mode})
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/tasks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra: daemon unreachable — start it with 'hydra serve' (", err, ")")
		os.Exit(1)
	}
	defer resp.Body.Close()
	var out struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.ID == "" {
		fmt.Fprintln(os.Stderr, "hydra: task rejected:", out.Error)
		os.Exit(1)
	}
	fmt.Printf("Queued task %s in %s (%s)\n", out.ID, abs, *mode)
	fmt.Println("Watch it: 'hydra tasks' or the web dashboard")
}
