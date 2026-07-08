package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"hydra/internal/core"
	"hydra/internal/notify"
)

// hookInput is the JSON Claude Code writes to a hook's stdin.
// Fields not present for a given event type simply stay empty.
type hookInput struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Message        string `json:"message"`   // Notification
	ToolName       string `json:"tool_name"` // PreToolUse / PostToolUse
	Prompt         string `json:"prompt"`    // UserPromptSubmit
}

// runHook must be bulletproof: whatever happens, exit 0 with no stdout.
// Non-zero exits or stdout from hooks can block tools or inject context
// into the Claude session that triggered us.
func runHook() {
	defer func() {
		recover()
		os.Exit(0)
	}()

	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 10<<20))
	if err != nil {
		return
	}
	var in hookInput
	if json.Unmarshal(raw, &in) != nil || in.HookEventName == "" {
		return
	}

	// Hooks inherit the Claude process's environment: when the session runs
	// inside tmux, TMUX_PANE identifies the exact pane — which is what lets
	// hydra attach to it and answer prompts, even for sessions the user
	// started in tmux by hand.
	var pane string
	if os.Getenv("TMUX") != "" {
		pane = os.Getenv("TMUX_PANE")
	}

	host, _ := os.Hostname()
	core.Append(core.Event{
		TS:         time.Now(),
		Host:       host,
		Event:      in.HookEventName,
		SessionID:  in.SessionID,
		CWD:        in.CWD,
		Transcript: in.TranscriptPath,
		Message:    in.Message,
		Tool:       in.ToolName,
		TmuxPane:   pane,
		HeadID:     os.Getenv("HYDRA_HEAD_ID"), // set for sessions launched as hydra heads
	})

	project := filepath.Base(in.CWD)
	if project == "." || project == "" {
		project = "?"
	}
	switch in.HookEventName {
	case "Notification":
		msg := in.Message
		if msg == "" {
			msg = "Waiting for your input"
		}
		notify.Send("🔴 Claude needs you — "+project, msg, true)
	case "Stop":
		notify.Send("✅ Claude finished — "+project, "Session is ready for your next instruction", false)
	}
}
