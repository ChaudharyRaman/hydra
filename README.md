# HYDRA 🐍

> **Many heads. One brain.**

A terminal-first mission control for people who run 10–15 Claude Code sessions at once and have no idea which one needs them.

## The problem

You run Claude Code in 10–15 terminals simultaneously. Right now you:

- Alt-tab through windows hunting for the one that's waiting for your input
- Miss permission prompts for minutes (Claude sits idle while you pay attention elsewhere)
- Have no overview of what each session is doing, how long it's been running, or what it costs
- Lose track of which project each terminal even belongs to
- Restart work that already finished because you forgot a session completed

Hydra turns that chaos into a single dashboard: every session, its live state (**working / needs you / done / idle**), one keypress to jump into any of them.

## The core insight

Claude Code already emits everything needed to build this — no forking, no hacks:

1. **Hooks** fire shell commands on every lifecycle event (needs permission, finished responding, session start/end). This gives *real-time semantic state*.
2. **Session transcripts** are append-only JSONL files in `~/.claude/projects/`, updated live. This gives *content and history*.
3. **tmux** gives *control* — spawn sessions inside tmux and you can list, watch, jump to, and type into any of them programmatically.

Hydra = a small Go daemon that collects (1) and (2), plus a Bubble Tea TUI that renders the fleet and uses (3) to control it.

## Architecture (target)

```
┌─────────────────────────────────────────────────────┐
│  hydra (TUI dashboard — Go + Bubble Tea)             │
│  fleet view · session detail · jump/attach · notify  │
└──────────────────────┬──────────────────────────────┘
                       │ unix socket / local API
┌──────────────────────┴──────────────────────────────┐
│  hydrad (daemon — Go)                                │
│  • hook event receiver (state machine per session)   │
│  • JSONL transcript watcher (fsnotify)               │
│  • tmux controller (spawn / attach / send-keys)      │
│  • notifier (desktop / push / webhook)               │
└──────────────────────────────────────────────────────┘
        ▲                    ▲                  ▲
   Claude Code hooks    ~/.claude/projects   tmux server
   (all sessions)       (*.jsonl live)       (managed heads)
```

## Status

**The console (`hydra`) is the primary UI** — a full-screen split, like a Claude-aware terminal multiplexer. Left: a `PROJECTS & AGENTS` sidebar of every session with a live status dot (● Running / ● Pending Input / ● Stopped / ● Inactive). Right: the **live, typeable terminal** of the selected head — a real Claude session embedded in the pane, not a transcript.

```
 ☁ HYDRA · many heads, one brain
╭──────────────────────────────╮╭─────────────────────────────────────────────────────╮
│PROJECTS & AGENTS             ││[echo /home/raman/dev/projects/echo]                 │
│● ECHO                        ││ ⏺ I'll add the webhook retry logic now.             │
│   Running 2m                 ││                                                     │
│● MR_TRADER                   ││ ● Bash(go test ./...)                               │
│   Pending Input 14s          ││   ⎿ ok  hydra/internal/core  0.01s                  │
│● HYDRA                       ││                                                     │
│   Ready 5s                   ││ Do you want to proceed?                             │
│● GCP-LOG-ARCHIVE             ││ ❯ 1. Yes    2. No                                   │
│   Inactive 17h               ││                                                     │
╰──────────────────────────────╯╰─────────────────────────────────────────────────────╯
 Info: MR_TRADER · Pending Input      ↑/↓ Select · Enter Focus · Ctrl+N New · q Quit
```

Each head is a child process hydra runs under its own PTY, with a virtual-terminal emulator (`charmbracelet/x/vt`) turning its byte stream into the rendered screen. `Enter` focuses the terminal — every keystroke (including Ctrl+C to interrupt Claude, Esc, arrows) goes straight to that session, with the cursor drawn where your typing lands; `Ctrl+Q` detaches back to the sidebar. `Ctrl+N` spawns a new head in a directory you pick.

**Hydra tracks only the heads it spawns.** The sidebar is exactly the set of sessions you launched through hydra (`Ctrl+N`, or `hydra new`) — each fully live and typeable. Sessions started in other terminals are deliberately left out of the console; the OS won't let one program drive another's terminal anyway. (The separate `hydra dash` / `hydra serve` views can still passively monitor every session on the machine via hooks, if you want that.)

`hydra dash` remains as a lighter, control-focused fleet view:

```
 HYDRA · many heads, one brain   1 needs-you · 3 working · 2 idle
╭──────────────────────────────────────────╮╭─────────────────────────────────────────────╮
│ needs-you mr_trader          14s 53150…  ││ mr_trader · 53150482 · needs-you            │
│ working   echo                2s cd5a8…  ││ you    add webhook retries                  │
│ working   hydra               5s eb57c…  ││ ⚙ tool Bash go test ./...                   │
│°idle      echo            13h20m 62670…  ││   ↳    ok hydra/internal/core 0.01s         │
│                                          ││ claude Tests pass. Now the retry backoff…   │
╰──────────────────────────────────────────╯╰─────────────────────────────────────────────╯
 ↑/↓ select · PgUp/PgDn scroll · a ended on/off · r refresh · q quit
```

- Left pane: every session, urgent first, refreshed every second from the hook event log. `°` = backfilled pre-hydra session; `▸` = runs in tmux, hydra can control it.
- Right pane: live tail of the selected session's transcript (`p` toggles to the pane's actual live screen), parsed straight from `~/.claude/projects/**/*.jsonl`.
- **Control** (sessions inside tmux): `Enter` jumps into the session — `switch-client` when the dashboard is in tmux, nested attach otherwise. `y`/`n` answers a pending permission prompt remotely (`1` / `Escape`) — after verifying the dialog is actually on that pane's screen, so hydra never types into a shell blind.
- `hydra new [dir]` spawns a controllable head in the `hydra` tmux session. Any session you start inside tmux yourself is auto-controllable too: hooks inherit `$TMUX_PANE`, so the pane mapping is free. Make it the default: `alias cc='hydra new'`.

**The daemon (`hydra serve`)** adds the product layer:

- **Web dashboard** — a mobile-friendly page (embedded in the binary, zero external assets) showing the fleet, expandable transcripts, and **Approve/Reject buttons** on sessions that need you. Token-authenticated (`~/.hydra/token`, printed as a ready-to-open URL). Run with `-addr 0.0.0.0:7717` and open it on your phone via your LAN IP — approve permission prompts from the couch. The buttons hit the same screen-verified tmux path as the TUI, so remote answers are just as safe.
- **Headless task queue** — `hydra run -d ~/dev/projects/echo "add webhook retries"` queues a prompt; daemon workers (default 3, `-jobs N`) execute it via `claude -p --output-format stream-json`, capturing the result, cost, and full stream log to `~/.hydra/tasks/`. Permission mode per task (`-m acceptEdits` default). Desktop notification on done/failed; `hydra tasks` lists everything. Queued tasks survive daemon restarts.

Commands: `hydra` (console) · `hydra dash` · `hydra new [dir]` · `hydra serve [-addr] [-jobs]` · `hydra run [-d dir] [-m mode] "prompt"` · `hydra tasks` · `hydra status [-a]` · `hydra tail` · `hydra install` / `hydra uninstall`.
Console keys: `↑/↓` select · `Enter` focus terminal · `Ctrl+Q` detach · `Ctrl+N` new head (`Tab` in the prompt cycles Claude / plain shell / custom command like `ssh host`) · `R` rename · `Ctrl+X` close head · mouse-wheel / `Shift+PgUp`/`PgDn` scroll history · `F1` help · `F5` refresh · `q` quit. In a focused terminal all keys pass through to Claude/the shell, including `Ctrl+C`, `Ctrl+←/→` (word motion), `Ctrl+Backspace` (delete word), tab-completion, Ctrl+R reverse-search, and paste. Runs on Linux and macOS; native Windows builds via ConPTY (untested) and works today under WSL. See [`ROADMAP.md`](ROADMAP.md) for what's next (notably head persistence across quit).
Rebuild after changes: `go build -o ~/.hydra/bin/hydra ./cmd/hydra`

**Run the daemon on boot** (systemd user service, [`deploy/hydra.service`](deploy/hydra.service)):

```sh
mkdir -p ~/.config/systemd/user
cp ~/dev/projects/hydra/deploy/hydra.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now hydra
loginctl enable-linger $USER   # optional: keep it running when logged out
```

## Roadmap

- **Phase 0 — instant relief:** ✅ **done** — hook receiver + event log + notification fallback chain. Terminal-agnostic by design (see [`docs/COMPATIBILITY.md`](docs/COMPATIBILITY.md)).
- **Phase 1 — the dashboard:** ✅ **done** — `hydra dash` Bubble Tea TUI: live fleet view + transcript tail pane + backfill of pre-hydra sessions. Read-only, works with existing terminals.
- **Phase 2 — control:** ✅ **done** — `hydra new` spawns tmux heads; dashboard jumps to sessions (Enter), answers permission prompts remotely (y/n, screen-verified), live screen preview (p). Auto-detects any tmux-hosted session via `$TMUX_PANE` in hooks.
- **Phase 3 — the product:** ✅ **core done** — `hydra serve` daemon: web/phone dashboard with remote approvals, headless task queue over `claude -p` stream-json with per-task cost tracking. Still open: git-worktree task fanout, web push notifications, multi-machine aggregation, `--permission-prompt-tool` for true remote permission routing on headless tasks.

## Docs

- [`docs/SOLUTIONS.md`](docs/SOLUTIONS.md) — the full exploration: every possible approach, what is and isn't technically possible, prior art, and why this architecture wins.
- [`docs/COMPATIBILITY.md`](docs/COMPATIBILITY.md) — why Hydra works under every terminal type, the notification transport matrix, and the hook-safety guarantees (all verified by test).

## Stack

Go end-to-end. TUI: [Bubble Tea](https://github.com/charmbracelet/bubbletea) + Lip Gloss. File watching: fsnotify. No external services — everything local-first.
