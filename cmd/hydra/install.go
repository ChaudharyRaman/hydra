package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookedEvents are the lifecycle events hydra subscribes to. One command
// serves them all: the event name arrives in stdin JSON (hook_event_name).
var hookedEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "Notification", "Stop", "SessionEnd",
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func binPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hydra", "bin", "hydra")
}

func runInstall() {
	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	backup := settingsPath() + ".hydra-bak-" + time.Now().Format("20060102-150405")
	if data, err := os.ReadFile(settingsPath()); err == nil {
		if err := os.WriteFile(backup, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "hydra: backup failed, aborting:", err)
			os.Exit(1)
		}
		fmt.Println("Backed up settings to", backup)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	command := binPath() + " hook"
	added := 0
	for _, event := range hookedEvents {
		entries, _ := hooks[event].([]any)
		if containsHydra(entries) {
			continue
		}
		entries = append(entries, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command}},
		})
		hooks[event] = entries
		added++
	}
	settings["hooks"] = hooks

	if err := saveSettings(settings); err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	fmt.Printf("Installed hydra hooks for %d event(s): %s\n", added, strings.Join(hookedEvents, ", "))
	fmt.Println("\nNOTE: hooks are read at session startup. Already-running Claude sessions")
	fmt.Println("keep their old config — restart each one (or finish its task first) to")
	fmt.Println("bring it under hydra's watch. New sessions report automatically.")
}

func runUninstall() {
	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		fmt.Println("No hooks configured; nothing to remove.")
		return
	}
	removed := 0
	for event, v := range hooks {
		entries, _ := v.([]any)
		kept := entries[:0]
		for _, e := range entries {
			if isHydraEntry(e) {
				removed++
			} else {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	if err := saveSettings(settings); err != nil {
		fmt.Fprintln(os.Stderr, "hydra:", err)
		os.Exit(1)
	}
	fmt.Printf("Removed %d hydra hook entrie(s).\n", removed)
}

func loadSettings() (map[string]any, error) {
	settings := map[string]any{}
	data, err := os.ReadFile(settingsPath())
	if os.IsNotExist(err) {
		return settings, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", settingsPath(), err)
	}
	return settings, nil
}

func saveSettings(settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), append(data, '\n'), 0o644)
}

func containsHydra(entries []any) bool {
	for _, e := range entries {
		if isHydraEntry(e) {
			return true
		}
	}
	return false
}

func isHydraEntry(entry any) bool {
	m, _ := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hydra hook") {
			return true
		}
	}
	return false
}
