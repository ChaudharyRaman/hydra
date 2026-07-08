# Terminal Compatibility

Hydra's core claim: **it works identically under every terminal**, because it never talks to your terminal at all.

## Why terminal type is irrelevant for detection

Hooks are executed by the **Claude Code process itself**, inheriting the session's environment. GNOME Terminal, Kitty, Alacritty, a VS Code panel, an SSH connection — Claude Code doesn't behave differently in any of them, so Hydra's event stream is identical in all of them. There is no output scraping, no PTY spying, no window-manager tricks. The only thing that varies by environment is **how the notification reaches your eyeballs**, which is why `hydra hook` walks a transport fallback chain.

## The transport chain

For each notification, the first transport that works wins (the event is always logged to `~/.hydra/events.jsonl` first, so nothing is ever lost even if no transport works):

1. **macOS** → `osascript` → Notification Center
2. **Linux desktop** → `notify-send` (when `DISPLAY`/`WAYLAND_DISPLAY` is set) → freedesktop notifications; "needs you" is sent as `critical` so it stays on screen until dismissed
3. **WSL** → `wsl-notify-send`, falling back to BurntToast via `powershell.exe` → Windows toast
4. **Headless / SSH** → `tmux display-message` to every attached tmux client on the box

## The matrix

| You run Claude Code in… | Events detected? | Notification arrives via | Notes |
|---|---|---|---|
| GNOME Terminal / Konsole / xterm | ✅ | notify-send (desktop popup) | The default case on this machine — verified working |
| Kitty / Alacritty / WezTerm / Ghostty | ✅ | notify-send | GPU terminals set DISPLAY like any other |
| VS Code / Cursor integrated terminal | ✅ | notify-send | Hooks don't care that the parent is an editor |
| JetBrains IDE terminal | ✅ | notify-send | Same |
| Inside tmux (any outer terminal) | ✅ | notify-send, plus tmux status-line message as backup | Best of both |
| Inside GNU screen | ✅ | notify-send | Control features (Phase 2) target tmux only, but Phase 0 monitoring is fine |
| SSH **into** this machine from elsewhere | ✅ | tmux display-message (no DISPLAY over plain SSH) | Run remote sessions inside tmux to receive alerts; `hydra status` over SSH always works |
| SSH from this machine **to** a remote box | ⚠️ hooks fire on the *remote* box | remote's transports | Install hydra on the remote too; `host` field in events exists for future aggregation |
| WSL2 on Windows | ✅ | wsl-notify-send / BurntToast | Untested here (this box is native Linux); chain implemented |
| macOS Terminal / iTerm2 | ✅ | osascript | Untested here; chain implemented |
| Wayland-native terminals | ✅ | notify-send via WAYLAND_DISPLAY | Both env vars checked |
| Headless server, no tmux, no display | ✅ | *(none live)* | Events still logged; `hydra status` / `hydra tail` are your view |

## Failure-proofing (verified by test)

A hook that crashes, blocks, or prints could corrupt a Claude session's behavior. `hydra hook` is hardened accordingly, and each property below was tested on this machine:

- **Always exits 0** — even on garbage or empty stdin (non-zero exit from some hooks blocks tools or shows errors in-session)
- **Never writes to stdout** — stdout from a `UserPromptSubmit` hook is injected into the session's context; hydra emits zero bytes
- **Fast** — ~2ms per event; notification subprocesses carry a 3s hard timeout so a hung notify daemon can't stall Claude
- **Concurrent-safe** — event log uses `O_APPEND` writes under 4KB, atomic on Linux, so 15 sessions share one file without locks
- **Recover-wrapped** — a panic in hydra still exits 0 silently

## Known limits

- Sessions **already running** when `hydra install` ran keep their old hook config; they report nothing until restarted. (One-time cost at adoption.)
- **Control requires tmux**: jump (Enter) and remote answer (y/n) only work for sessions inside tmux — either spawned by `hydra new` or started inside tmux by hand (hooks pick up `$TMUX_PANE` automatically). Plain-terminal sessions stay observe-only; that's an OS boundary, not a hydra one.
- Desktop popups require a notification daemon (present on stock GNOME/KDE; minimal WMs may need `dunst`).
