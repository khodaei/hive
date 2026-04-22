package tui

import (
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// inputInsertChar inserts a character at the cursor position.
func (m *Model) inputInsertChar(s *string, ch string) {
	if m.inputCursor >= len(*s) {
		*s += ch
	} else {
		*s = (*s)[:m.inputCursor] + ch + (*s)[m.inputCursor:]
	}
	m.inputCursor += len(ch)
}

// inputBackspace deletes the character before the cursor.
func (m *Model) inputBackspace(s *string) {
	if m.inputCursor > 0 && len(*s) > 0 {
		*s = (*s)[:m.inputCursor-1] + (*s)[m.inputCursor:]
		m.inputCursor--
	}
}

// inputDeleteWord deletes the word before the cursor (Option+Backspace).
func (m *Model) inputDeleteWord(s *string) {
	if m.inputCursor == 0 {
		return
	}
	// Skip trailing spaces
	i := m.inputCursor - 1
	for i > 0 && (*s)[i] == ' ' {
		i--
	}
	// Skip word characters
	for i > 0 && (*s)[i-1] != ' ' {
		i--
	}
	*s = (*s)[:i] + (*s)[m.inputCursor:]
	m.inputCursor = i
}

// inputMoveLeft moves cursor one position left.
func (m *Model) inputMoveLeft() {
	if m.inputCursor > 0 {
		m.inputCursor--
	}
}

// inputMoveRight moves cursor one position right.
func (m *Model) inputMoveRight(s string) {
	if m.inputCursor < len(s) {
		m.inputCursor++
	}
}

// inputMoveWordLeft moves cursor to the start of the previous word.
func (m *Model) inputMoveWordLeft(s string) {
	if m.inputCursor == 0 {
		return
	}
	i := m.inputCursor - 1
	for i > 0 && s[i] == ' ' {
		i--
	}
	for i > 0 && s[i-1] != ' ' {
		i--
	}
	m.inputCursor = i
}

// inputMoveWordRight moves cursor to the end of the next word.
func (m *Model) inputMoveWordRight(s string) {
	i := m.inputCursor
	for i < len(s) && s[i] == ' ' {
		i++
	}
	for i < len(s) && s[i] != ' ' {
		i++
	}
	m.inputCursor = i
}

// inputMoveHome moves cursor to the start.
func (m *Model) inputMoveHome() {
	m.inputCursor = 0
}

// inputMoveEnd moves cursor to the end.
func (m *Model) inputMoveEnd(s string) {
	m.inputCursor = len(s)
}

// inputSetText sets the input text and moves cursor to the end.
func (m *Model) inputSetText(s *string, text string) {
	*s = text
	m.inputCursor = len(text)
}

// renderInput renders text with a cursor indicator.
func renderInputWithCursor(s string, cursor int) string {
	if cursor >= len(s) {
		return s + "█"
	}
	return s[:cursor] + "█" + s[cursor:]
}

// inputHandleKey handles common text input keys. Returns true if the key was handled.
// Pass the tea.KeyMsg to properly handle bracketed paste events.
func (m *Model) inputHandleKey(s *string, key string, msg ...tea.KeyMsg) bool {
	// Handle bracketed paste: insert the raw runes without bracket wrapping.
	if len(msg) > 0 && msg[0].Paste {
		text := string(msg[0].Runes)
		// Strip newlines — single-line inputs shouldn't break on paste.
		text = strings.ReplaceAll(text, "\n", " ")
		text = strings.ReplaceAll(text, "\r", "")
		m.inputInsertChar(s, text)
		return true
	}

	switch key {
	case "left":
		m.inputMoveLeft()
		return true
	case "right":
		m.inputMoveRight(*s)
		return true
	case "alt+left":
		m.inputMoveWordLeft(*s)
		return true
	case "alt+right":
		m.inputMoveWordRight(*s)
		return true
	case "home", "ctrl+a":
		m.inputMoveHome()
		return true
	case "end", "ctrl+e":
		m.inputMoveEnd(*s)
		return true
	case "alt+backspace":
		m.inputDeleteWord(s)
		return true
	case "backspace":
		m.inputBackspace(s)
		return true
	case "ctrl+v":
		// Paste from system clipboard
		if out, err := exec.Command("pbpaste").Output(); err == nil && len(out) > 0 {
			text := strings.ReplaceAll(string(out), "\n", " ")
			text = strings.ReplaceAll(text, "\r", "")
			m.inputInsertChar(s, text)
		}
		return true
	case "ctrl+u":
		// Delete to start of line
		*s = (*s)[m.inputCursor:]
		m.inputCursor = 0
		return true
	case "ctrl+k":
		// Delete to end of line
		*s = (*s)[:m.inputCursor]
		return true
	default:
		if len(key) == 1 && key[0] >= 32 {
			m.inputInsertChar(s, key)
			return true
		}
		// Handle multi-byte chars (e.g. pasted text)
		if len(key) > 1 && !strings.HasPrefix(key, "ctrl+") && !strings.HasPrefix(key, "alt+") {
			m.inputInsertChar(s, key)
			return true
		}
	}
	return false
}
