package main

import tea "github.com/charmbracelet/bubbletea"

// keyToBytes translates a Bubble Tea key event into the byte sequence a
// real terminal would deliver to the child process's stdin. Covers the keys
// an interactive Claude Code / shell session actually needs.
func keyToBytes(msg tea.KeyMsg) []byte {
	// Modifier combos Bubble Tea names but doesn't expose as simple types:
	// word-wise cursor movement and word deletion that shells/Claude expect.
	switch msg.String() {
	case "ctrl+right":
		return []byte("\x1b[1;5C")
	case "ctrl+left":
		return []byte("\x1b[1;5D")
	case "ctrl+up":
		return []byte("\x1b[1;5A")
	case "ctrl+down":
		return []byte("\x1b[1;5B")
	case "alt+right":
		return []byte("\x1b[1;3C")
	case "alt+left":
		return []byte("\x1b[1;3D")
	case "ctrl+h": // Ctrl+Backspace on most terminals -> delete previous word
		return []byte{0x17}
	case "alt+backspace": // readline backward-kill-word
		return []byte{0x1b, 0x7f}
	}
	switch msg.Type {
	case tea.KeyRunes:
		b := []byte(string(msg.Runes))
		if msg.Alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyShiftTab:
		return []byte("\x1b[Z")
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	}
	// Ctrl-A..Ctrl-Z and friends map to ASCII control codes 1..31, which is
	// exactly the numeric value of these key types in Bubble Tea.
	if msg.Type >= 1 && msg.Type <= 31 {
		return []byte{byte(msg.Type)}
	}
	return nil
}
