package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// Line is one display-ready item from a session transcript.
type Line struct {
	When time.Time
	Kind string // you | claude | tool | result | info
	Text string
}

// transcriptLine covers the shapes Claude Code writes to session JSONL.
// Content is either a plain string or an array of typed blocks, so it
// stays raw until we know which.
type transcriptLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Summary   string    `json:"summary"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`    // tool_use
	Input   json.RawMessage `json:"input"`   // tool_use
	Content json.RawMessage `json:"content"` // tool_result
}

// Tail parses the last maxBytes of a transcript into display lines.
// Every parse is best-effort: unknown shapes are skipped, never fatal.
func Tail(path string, maxBytes int64) []Line {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	if fi, err := f.Stat(); err == nil && fi.Size() > maxBytes {
		f.Seek(fi.Size()-maxBytes, io.SeekStart)
		// Drop the partial first line after seeking mid-file.
		br := bufio.NewReader(f)
		br.ReadBytes('\n')
		return parseLines(br)
	}
	return parseLines(bufio.NewReader(f))
}

func parseLines(r *bufio.Reader) []Line {
	var out []Line
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4<<20), 4<<20)
	for sc.Scan() {
		var tl transcriptLine
		if json.Unmarshal(sc.Bytes(), &tl) != nil {
			continue
		}
		switch tl.Type {
		case "summary":
			if tl.Summary != "" {
				out = append(out, Line{Kind: "info", Text: "≡ " + tl.Summary})
			}
		case "user":
			out = append(out, userLines(tl)...)
		case "assistant":
			out = append(out, assistantLines(tl)...)
		}
	}
	return out
}

func userLines(tl transcriptLine) []Line {
	// Plain-string content is a real human prompt.
	var text string
	if json.Unmarshal(tl.Message.Content, &text) == nil {
		return []Line{{When: tl.Timestamp, Kind: "you", Text: clean(text)}}
	}
	var blocks []contentBlock
	if json.Unmarshal(tl.Message.Content, &blocks) != nil {
		return nil
	}
	var out []Line
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, Line{When: tl.Timestamp, Kind: "you", Text: clean(b.Text)})
		case "tool_result":
			if snippet := resultSnippet(b.Content); snippet != "" {
				out = append(out, Line{When: tl.Timestamp, Kind: "result", Text: snippet})
			}
		}
	}
	return out
}

func assistantLines(tl transcriptLine) []Line {
	var blocks []contentBlock
	if json.Unmarshal(tl.Message.Content, &blocks) != nil {
		return nil
	}
	var out []Line
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := clean(b.Text); t != "" {
				out = append(out, Line{When: tl.Timestamp, Kind: "claude", Text: t})
			}
		case "tool_use":
			out = append(out, Line{When: tl.Timestamp, Kind: "tool", Text: b.Name + " " + inputSnippet(b.Name, b.Input)})
		}
	}
	return out
}

// inputSnippet picks the most human-readable part of a tool call's input.
func inputSnippet(tool string, raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "pattern", "prompt", "description", "url"} {
		if v, ok := in[key].(string); ok && v != "" {
			return clean(v)
		}
	}
	compact, _ := json.Marshal(in)
	return clean(string(compact))
}

func resultSnippet(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return clean(text)
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return clean(b.Text)
			}
		}
	}
	return ""
}

// clean flattens a possibly multi-line string into one display line.
func clean(s string) string {
	s = strings.TrimSpace(s)
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 {
		s = s[:i] + " ⏎ …"
	}
	return s
}
