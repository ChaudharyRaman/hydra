# HYDRA — Solution Space Exploration

The full map: what Claude Code exposes, what is and isn't technically possible, every viable approach with trade-offs, prior art, and the recommended path.

*Environment this was validated against: Linux, tmux 3.2a installed, Claude Code with 54 session transcripts across 5 projects in `~/.claude/projects/`, no hooks configured yet.*

---

## 1. The problem, precisely

Running 10–15 concurrent Claude Code sessions creates five distinct pains. Any solution should be scored against these:

| # | Pain | What "solved" looks like |
|---|------|--------------------------|
| P1 | **Attention routing** — which session needs me *right now*? | Instant signal when any session hits a permission prompt or finishes |
| P2 | **Fleet visibility** — what is each session doing? | One screen: project, current task, state, duration, cost |
| P3 | **Navigation** — getting to the right terminal | One keypress from dashboard to any session |
| P4 | **Control** — answering prompts, starting/stopping work | Approve/deny/reply without hunting for the window |
| P5 | **Memory** — what happened across sessions today? | Searchable history, per-project summaries, cost rollup |

---

## 2. Ground truth: what Claude Code exposes

Everything below is stock Claude Code — no forking or patching needed.

### 2.1 Session transcripts (live JSONL)

Every session appends to `~/.claude/projects/<path-slug>/<session-uuid>.jsonl` in real time. Each line is a JSON event: user messages, assistant messages, tool calls, tool results, token usage. **This is the single richest data source** — a passive watcher gets full content of every session on the machine, including sessions started in terminals we don't control.

- Gives: P2 (what it's doing), P5 (history), partial P1 (activity detection by mtime)
- Doesn't give: clean "waiting for permission" state (that's inferable but ugly — hooks do it properly), and no control.

### 2.2 Hooks (the real-time nervous system)

Claude Code hooks (configured in `~/.claude/settings.json`) run arbitrary shell commands on lifecycle events. The ones that matter for Hydra:

| Hook | Fires when | Hydra use |
|------|-----------|-----------|
| `Notification` | Claude needs permission or has been idle waiting for input | **P1 — the money hook.** "Session X needs you" |
| `Stop` | Claude finishes responding | "Session X is done / ready for next instruction" |
| `SessionStart` / `SessionEnd` | Session lifecycle | Fleet registry — sessions self-register |
| `PreToolUse` / `PostToolUse` | Around every tool call | Live "currently running: `go test ./...`" status |
| `UserPromptSubmit` | User sends a prompt | Mark session as "working" |
| `SubagentStop`, `PreCompact` | Subagent finished / context compaction | Nice-to-have telemetry |

Each hook receives JSON on stdin including `session_id`, `transcript_path`, and `cwd` — exactly the correlation keys a daemon needs. A hook that does `curl --unix-socket /run/hydra.sock -d @-` turns every Claude session on the machine into a self-reporting node **regardless of which terminal it runs in**.

- Gives: P1 completely, P2 state machine (working → needs-input → done)
- Cost: one-time settings.json entry; hooks apply globally to all sessions automatically.

### 2.3 tmux (the control plane)

tmux is the only clean way to get P3 and P4. If a Claude session runs inside a tmux session/window:

- `tmux list-sessions` / `list-windows` — enumerate the fleet
- `tmux switch-client -t X` / `attach` — jump to any head instantly
- `tmux send-keys -t X "y" Enter` — answer prompts remotely
- `tmux capture-pane -t X -p` — read the actual screen (what the user sees, including the prompt UI)
- `tmux new-session -d 'claude'` — spawn heads programmatically

### 2.4 Other native capabilities

- **`claude --resume <id>` / `--continue`** — reattach to any past session; the dashboard can offer "resume" on dead sessions.
- **Headless mode: `claude -p "..." --output-format stream-json`** — run Claude Code as a subprocess with structured output. Foundation for full orchestration (Phase 3).
- **Agent SDK** — programmatic Claude Code with tool control, for building a true multi-agent product later.
- **Statusline** — a configurable status bar inside each session; can display "hydra: 3 sessions need attention" *inside* every terminal.
- **OpenTelemetry** (`CLAUDE_CODE_ENABLE_TELEMETRY=1`) — metrics export (tokens, cost, session counts) for the analytics layer.
- **`~/.claude/history.jsonl`** — global prompt history across sessions.

---

## 3. What is NOT possible (and the workarounds)

Be honest about the walls so we don't design into them:

| ✗ Impossible / impractical | Why | Workaround |
|---|---|---|
| Reading the screen of a plain terminal window (GNOME Terminal, Kitty tab, etc.) we didn't spawn | The OS gives no access to another process's PTY buffer | Hooks + JSONL give the *semantic* state without needing pixels; for the visual screen, the session must live in tmux |
| Typing into a terminal window we don't own | Same PTY ownership problem. `xdotool` window-focus hacks exist but are fragile, X11-only, and misfire | tmux `send-keys` — but only for tmux-managed sessions. Hence Phase 2 = "adopt heads into tmux" |
| Getting "waiting for permission" purely from JSONL | The transcript shows the tool call but the pending-approval state isn't a clean event in the file | `Notification` hook fires exactly at that moment — this is the designed mechanism |
| Two processes driving one Claude session interactively | A session has one PTY / one driver | Dashboard *observes* everything but *controls* via tmux or via owning headless sessions |
| Approving a permission prompt from another machine/phone, for a plain-terminal session | Combination of the above | Phase 3: sessions Hydra spawns headless can route permission decisions anywhere (SDK `canUseTool` callback) |
| Retroactive state for sessions started before hydrad was running | Hooks only fire while configured; no replay | JSONL backfill: parse existing transcripts on daemon start (we already have 54 of them to test against) |

**The one-line takeaway:** *observation* of every session is fully possible today (hooks + JSONL, terminal-agnostic); *control* requires the session to live inside something we manage (tmux now, headless/SDK later).

---

## 4. The solution options

### Option A — Pure tmux discipline (no code)

Move all Claude sessions into one tmux session, one window each. Use `choose-tree` (prefix+s/w) to navigate, `monitor-activity`/`monitor-silence` for crude alerts, names like `echo-api`, `mr-trader-backtest`.

- **Solves:** P3 well, P2/P1 poorly (activity flags, no semantics — "output happened" ≠ "needs you")
- **Effort:** zero. **Verdict:** do this *today* as the substrate; it's what Phase 2 builds on. Not a product.

### Option B — Passive TUI dashboard (JSONL watcher)

Go + Bubble Tea app watching `~/.claude/projects/**/*.jsonl` with fsnotify. Table of sessions: project, last message snippet, tokens, last-activity age. Detail view = live transcript tail.

- **Solves:** P2, P5. P1 only heuristically (stale mtime ≈ waiting?). No P3/P4.
- **Effort:** small-medium. Zero configuration for the user — works on any machine with Claude Code instantly.
- **Verdict:** great skeleton and demo, insufficient alone. Becomes the *read path* of the real product.

### Option C — Hooks → daemon → events (the nervous system)

`hydrad` listens on a unix socket. A global hook config POSTs every `Notification` / `Stop` / `SessionStart` / `SessionEnd` / tool event to it. Daemon keeps a state machine per session: `working → needs-input → done → idle/dead`. Emits desktop notifications (`notify-send`), and serves state to any frontend.

- **Solves:** P1 completely and correctly (the `Notification` hook is *designed* for this), P2 states.
- **Effort:** small. This is the highest value-per-line-of-code in the whole design.
- **Verdict:** **non-negotiable core.** Works with all 15 terminals as they are today — no workflow change.

### Option D — tmux-native orchestrator (C + B + control)

Hydra owns a tmux session; each Claude "head" is a window Hydra spawned (`hydra new echo`, or `hydra adopt` re-parents your workflow gradually). Dashboard adds: **Enter to jump to a head, `y`/`n` to answer a pending permission via `send-keys`, `capture-pane` preview, spawn/kill heads.**

- **Solves:** P1–P5, all of them. You keep the full native Claude Code interactive UI in every head.
- **Cost:** sessions must start via/inside tmux (a shell alias makes this invisible: `alias cc='hydra new'`).
- **Verdict:** **the sweet spot.** This is Hydra v1, the thing worth showing people.

### Option E — Full headless orchestrator (Agent SDK)

Hydra spawns every session itself via `claude -p --output-format stream-json` or the Agent SDK, and renders all UI itself. No terminals at all — Hydra *is* the interface. Permission decisions become API callbacks, routable anywhere (TUI, web, phone).

- **Solves:** everything, plus programmable workflows: task queues, "run these 5 tasks across worktrees, wake me for failures", auto-retry, remote approval.
- **Cost:** large. You re-implement the interactive UX Claude Code already perfected (slash commands, plan mode rendering, images, …), and you're chasing a moving target.
- **Verdict:** the *product* horizon (Phase 3), not the starting point. D gives 90% of the value at 20% of the cost; E is where differentiation lives later.

### Option F — Web dashboard / mobile companion

Same daemon, browser frontend + push notifications. Approve a permission from your phone while away from the desk.

- **Verdict:** additive layer once C/D exist; trivially enabled by the daemon architecture (one more frontend on the same API). Pairs with E for remote *control*, with C for remote *visibility*.

### Option G — Telemetry/analytics layer (OTEL)

Enable Claude Code's OTEL export → local collector → cost per project/day, tokens, session counts. Complements everything; solves the "what did today cost me" slice of P5.

- **Verdict:** bolt-on, Phase 2/3. Cheap and impressive on a dashboard.

---

## 5. Scenario matrix

Every concrete scenario, and which option delivers it:

| Scenario | A tmux | B JSONL | C hooks | D orchestrator | E headless |
|---|---|---|---|---|---|
| "Ping me the instant any session needs permission" | — | ~ (heuristic) | ✅ | ✅ | ✅ |
| "Show all 15 sessions and their states on one screen" | ~ (names only) | ✅ | ✅ | ✅ | ✅ |
| "Jump to the session that pinged me, one keypress" | ✅ (manual) | — | — | ✅ | ✅ |
| "Approve/deny from the dashboard without switching" | — | — | — | ✅ | ✅ |
| "Approve from my phone" | — | — | ~ (see only) | ~ | ✅ |
| "What did session 7 do while I was at lunch?" | — | ✅ | ~ | ✅ | ✅ |
| "Works with my existing 15 plain terminals, zero change" | — | ✅ | ✅ | — (needs tmux) | — |
| "Spawn 5 sessions on 5 worktrees and queue tasks" | ~ (manual) | — | — | ~ | ✅ |
| "Cost/token rollup per project per day" | — | ✅ | ~ | ✅ | ✅ |
| "Survive a reboot, resume everything" | ~ | ✅ (`--resume`) | — | ✅ | ✅ |

(~ = partially / with effort)

---

## 6. Prior art (know the landscape before building)

Worth studying — and worth beating:

- **claude-squad** — Go TUI managing multiple AI agents in tmux + git worktrees. Closest to Option D; validated the demand.
- **ccmanager** — TUI session manager for Claude Code with worktree support; infers state from output, i.e. weaker than the hooks approach.
- **Crystal / Conductor** — desktop (Electron/Mac) apps running parallel Claude Code sessions in worktrees. GUI-first, not terminal-first.
- **Vibe Kanban** — kanban board where cards are coding-agent tasks. The "task queue" end of Option E.
- **VibeTunnel / happy** — browser/mobile access to terminal agents. Option F territory.

**Hydra's opening:** none of these lead with the **hooks-based real-time state machine** (most scrape terminal output or poll), few are truly terminal-native + daemon-backed, and none treat "existing plain terminals" as first-class via passive mode (C+B works with zero workflow change — unique adoption story). Also: it's your daily-driver problem, which is the best product-founder position there is.

---

## 7. The recommended way out

**Architecture: C + B as the daemon core, D as the flagship UI, E/F as the product horizon.**

### Phase 0 — instant relief (this weekend, ~50 lines)
Hook entries in `~/.claude/settings.json` for `Notification` + `Stop` → tiny script → `notify-send "Claude [echo] needs you"`. No dashboard yet; the pain drops 60% immediately. Also start `hydrad` as a skeleton: append every hook event to `~/.hydra/events.jsonl` — data collection starts day one.

### Phase 1 — the dashboard (1–2 weeks)
`hydrad`: unix-socket API, per-session state machine fed by hooks, JSONL watcher for content + backfill of pre-existing sessions. `hydra`: Bubble Tea fleet view — state-colored rows (🔴 needs you / 🟢 working / ⚪ done / zzz idle), sorted by "needs attention first", detail pane with live transcript tail. **Read-only, works with all existing terminals.**

### Phase 2 — control (2–3 weeks)
tmux integration: `hydra new [name]` spawns heads, Enter attaches, `y/n` answers pending permissions via `send-keys`, `capture-pane` live preview, kill/respawn/resume. Statusline module + OTEL cost panel. `alias cc='hydra new'` completes migration painlessly.

### Phase 3 — the product
Headless heads via Agent SDK, task queue ("run these across 5 worktrees"), web + phone frontends with remote approval, per-project daily digests, multi-machine support. This is where Hydra stops being a tool and becomes the platform.

### Why this order wins
1. **Value in days, not months** — Phase 0 alone beats the status quo.
2. **Nothing thrown away** — the daemon/event model from Phase 0 is the same one Phase 3 runs on.
3. **Adoption has no cliff** — passive mode needs zero workflow change; tmux mode is opt-in per session.
4. **Go end-to-end** — daemon, TUI, tmux control, JSONL parsing are all Go-native strengths (Bubble Tea, fsnotify, os/exec).

---

## 8. Open questions (decide during Phase 1)

- **Head identity:** key sessions by `session_id`, but sessions resume/branch — need a stable "head" concept mapping many session_ids to one logical workstream (likely `cwd` + tmux window).
- **Hook latency/failure:** hooks run synchronously-ish; keep the POST fire-and-forget with a 100ms timeout so a dead daemon never slows Claude.
- **Multi-machine:** local-first now; the event schema should carry a `host` field from day one so it's forwardable later.
- **Permission granularity:** in Phase 2, `send-keys y` answers whatever is focused — capture-pane verification before sending, to never blind-approve the wrong prompt.
