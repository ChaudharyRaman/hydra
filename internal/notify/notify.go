package notify

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// notify delivers a message through the first transport that works in the
// current environment. The event is already persisted before this runs, so
// delivery is best-effort and must never block a Claude session for long.
//
// Chain, in order:
//  1. macOS:            osascript notification center
//  2. Linux desktop:    notify-send (needs DISPLAY or WAYLAND_DISPLAY)
//  3. WSL:              wsl-notify-send, else BurntToast via powershell.exe
//  4. headless/SSH:     tmux display-message to every attached tmux client
func Send(title, body string, critical bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if runtime.GOOS == "darwin" && hasCmd("osascript") {
		script := `display notification ` + appleQuote(body) + ` with title ` + appleQuote(title)
		if run(ctx, "osascript", "-e", script) {
			return
		}
	}

	if hasCmd("notify-send") && (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") {
		urgency := "normal"
		if critical {
			urgency = "critical"
		}
		if run(ctx, "notify-send", "-a", "Hydra", "-u", urgency, title, body) {
			return
		}
	}

	if isWSL() {
		if hasCmd("wsl-notify-send") && run(ctx, "wsl-notify-send", "--category", "Hydra", title+": "+body) {
			return
		}
		if hasCmd("powershell.exe") && run(ctx, "powershell.exe", "-NoProfile", "-Command",
			"New-BurntToastNotification -Text "+psQuote(title)+","+psQuote(body)) {
			return
		}
	}

	// Last resort: reach whoever is attached to a tmux session on this box
	// (covers SSH into a headless server).
	if hasCmd("tmux") {
		out, err := exec.CommandContext(ctx, "tmux", "list-clients", "-F", "#{client_name}").Output()
		if err != nil {
			return
		}
		msg := title + " — " + body
		for _, client := range strings.Fields(string(out)) {
			exec.CommandContext(ctx, "tmux", "display-message", "-c", client, msg).Run()
		}
	}
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) bool {
	return exec.CommandContext(ctx, name, args...).Run() == nil
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}

func appleQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
