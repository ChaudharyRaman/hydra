# Hydra Roadmap

What's done, and what's deliberately still ahead. This file is kept honest —
items are only moved to "Done" once verified, not once written.

## Done & verified

- **Phase 0–3 backend:** hook receiver + event log, desktop notifications with
  a cross-platform fallback chain, fleet state machine, `hydra dash`, tmux
  control, `hydra serve` daemon (web dashboard, remote approvals, headless task
  queue). See `README.md`.
- **The console (`hydra`):** sidebar of hydra-spawned heads + a live, typeable
  embedded terminal (PTY + `charmbracelet/x/vt`), cursor overlay, scrollback
  via mouse wheel / Shift+PgUp, F1 help, F5 refresh.
- **Shell features inside heads:** tab-completion, Ctrl+R reverse search,
  history, syntax highlighting, word motion (Ctrl+←/→), word delete
  (Ctrl+Backspace) — all work because a head is a real shell under a real PTY.
  Verified on Linux.
- **Head types:** Claude, plain shell, or a custom command (e.g. `ssh host`)
  chosen with Tab in the new-head prompt. Rename with `R`.
- **Bracketed paste** forwarded to the focused head.
- **Text selection & copy:** drag the mouse over the terminal pane to select;
  on release the text is copied to the system clipboard via OSC 52 (iTerm2,
  SSH-friendly) and the native tool (xclip/pbcopy/clip). Verified end-to-end.
- **Scrollback search:** `/` searches a head's whole history (reverse order),
  jumps to matches, `n`/`N` cycle, matches highlighted.

## Next up (highest value first)

### 1. Head persistence across quit — THE priority
Today, quitting the console kills every head (`Manager.CloseAll`). A daily
driver must not lose work. Plan: back each head with a detached process the
console *attaches* to rather than *owns*, so it survives.
- **Approach:** run each head's real work inside a persistent `tmux` session
  (`hydra_<id>`); the console's PTY runs `tmux attach` to it. Quitting the
  console drops the attach, not the work. On launch, discover existing
  `hydra_*` sessions and offer to reattach.
- Alternative: split the console into a daemon (owns PTYs) + a thin client.

### 2. Verify macOS / iTerm2 and native Windows
The code is already cross-platform (`creack/pty` covers Darwin + Windows
ConPTY; shell selection switches to PowerShell on Windows). But it has only
been *run* on Linux. Needs a real test pass on macOS/iTerm2 and Windows, plus
a launchd plist to mirror the Linux systemd unit.

### 3. Selection polish
Selection + copy works; still nice-to-have: word/line double/triple-click,
select across scrollback while auto-scrolling, and keyboard-driven selection
for the mouse-averse. Note: because hydra captures the mouse, the outer
terminal's *native* selection is bypassed — in iTerm2 hold ⌥ (Option), in
GNOME Terminal hold Shift, to select natively instead.

## Known limitations (by design or not yet addressed)

- **Fidelity through double-emulation:** iTerm2 inline images, Kitty graphics,
  sixel, and OSC 8 hyperlinks do not pass through the embedded emulator. True
  color is modelled but the round-trip is unverified. Complex full-screen TUIs
  (vim, htop) inside a head are untested.
- **Mouse to child:** the console captures the mouse for scrolling, so programs
  inside a head don't receive mouse events (Claude is keyboard-driven, so this
  rarely matters).
- **Performance at scale:** 15 heads with heavy continuous output is untested.
- **Autostart:** systemd user unit is Linux-only.
