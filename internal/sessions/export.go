// Package sessions exports fx sessions as markdown for search and review.
//
// fx keeps each session as an event log, and most of it is tool output. The
// export keeps the conversation: the user's messages, the agent's replies,
// and one line per tool call. That is a few percent of the log, small enough
// to grep and to read whole.
package sessions

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Stats counts what one export did.
type Stats struct {
	Written   int
	Unchanged int
	Skipped   int
}

type indexEntry struct {
	Op        string `json:"op"`
	ID        string `json:"id"`
	Role      string `json:"role"`
	Title     string `json:"title"`
	Workspace string `json:"workspace"`
	Created   int64  `json:"created"`
	Updated   int64  `json:"updated"`
}

type event struct {
	Kind      string          `json:"kind"`
	Type      string          `json:"type"`
	TS        int64           `json:"ts"`
	Workspace string          `json:"workspace"`
	Data      json.RawMessage `json:"data"`
}

type exported struct {
	Path  string `json:"path"`
	Mtime int64  `json:"mtime"`
}

const stateFile = ".export-state.json"

// Export writes every changed root session under sessionsDir to archiveDir.
// force rewrites sessions that have not changed.
func Export(sessionsDir, archiveDir string, force bool) (Stats, error) {
	var stats Stats

	index, err := readIndex(filepath.Join(sessionsDir, "index.jsonl"))
	if err != nil {
		return stats, err
	}

	state := map[string]exported{}
	statePath := filepath.Join(archiveDir, stateFile)
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &state)
	}

	for id, entry := range index {
		if entry.Role != "" && entry.Role != "root" {
			continue
		}

		logPath := filepath.Join(sessionsDir, id, "log.jsonl")
		info, err := os.Stat(logPath)
		if err != nil {
			continue
		}

		prev, seen := state[id]
		if seen && !force && prev.Mtime == info.ModTime().UnixNano() {
			stats.Unchanged++
			continue
		}

		doc, err := render(logPath, entry)
		if err != nil {
			return stats, fmt.Errorf("session %s: %w", id, err)
		}
		if doc == "" {
			stats.Skipped++
			continue
		}

		rel := fileName(entry)
		path := filepath.Join(archiveDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return stats, err
		}
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			return stats, err
		}
		// A session whose title changed moves to a new file. The state keeps
		// paths relative to the archive, so compare them that way.
		if seen && prev.Path != "" && prev.Path != rel {
			_ = os.Remove(filepath.Join(archiveDir, prev.Path))
		}

		state[id] = exported{Path: rel, Mtime: info.ModTime().UnixNano()}
		stats.Written++
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return stats, err
	}
	return stats, os.WriteFile(statePath, data, 0o644)
}

// Recent returns the archive paths of exported sessions that changed after
// cutoff, oldest first. Run Export first.
func Recent(sessionsDir, archiveDir string, cutoff time.Time) ([]string, error) {
	index, err := readIndex(filepath.Join(sessionsDir, "index.jsonl"))
	if err != nil {
		return nil, err
	}

	state := map[string]exported{}
	if data, err := os.ReadFile(filepath.Join(archiveDir, stateFile)); err == nil {
		_ = json.Unmarshal(data, &state)
	}

	type item struct {
		path    string
		updated int64
	}
	var items []item
	for id, entry := range index {
		e, ok := state[id]
		if !ok || entry.Updated <= cutoff.UnixMilli() {
			continue
		}
		items = append(items, item{filepath.Join(archiveDir, e.Path), entry.Updated})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].updated < items[b].updated })

	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.path
	}
	return paths, nil
}

// readIndex returns the last entry of each live session in fx's index.
func readIndex(path string) (map[string]indexEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]indexEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	index := map[string]indexEntry{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var e indexEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.ID == "" {
			continue
		}
		switch e.Op {
		case "put":
			index[e.ID] = e
		case "del":
			delete(index, e.ID)
		}
	}
	return index, scanner.Err()
}

// render returns the session as markdown, or "" when the user never wrote.
func render(logPath string, entry indexEntry) (string, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body strings.Builder
	workspace := entry.Workspace
	users := 0
	replying := false

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var e event
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		if e.Kind == "session_created" && workspace == "" {
			workspace = e.Workspace
		}
		if e.Kind != "item" {
			continue
		}

		switch e.Type {
		case "user":
			text := textOf(e.Data)
			if text == "" {
				continue
			}
			users++
			replying = false
			fmt.Fprintf(&body, "## %s user\n\n%s\n\n", clock(e.TS), text)

		case "assistant":
			text := textOf(e.Data)
			if text == "" {
				continue
			}
			if !replying {
				body.WriteString("### fx\n\n")
				replying = true
			}
			fmt.Fprintf(&body, "%s\n\n", text)

		case "tool_call":
			if !replying {
				body.WriteString("### fx\n\n")
				replying = true
			}
			fmt.Fprintf(&body, "- `%s`\n\n", toolLine(e.Data))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if users == 0 {
		return "", nil
	}

	var doc strings.Builder
	fmt.Fprintf(&doc, "# %s\n\n", titleOf(entry))
	fmt.Fprintf(&doc, "- session: %s\n", entry.ID)
	fmt.Fprintf(&doc, "- started: %s\n", time.UnixMilli(entry.Created).Format("2006-01-02 15:04"))
	if workspace != "" {
		fmt.Fprintf(&doc, "- workspace: %s\n", workspace)
	}
	doc.WriteString("\n")
	doc.WriteString(body.String())
	return doc.String(), nil
}

func textOf(data json.RawMessage) string {
	var d struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(data, &d)
	return strings.TrimSpace(d.Text)
}

// toolLine summarizes a tool call on one line: its name and its main
// argument.
func toolLine(data json.RawMessage) string {
	var d struct {
		Tool string `json:"tool_name"`
		Args string `json:"arguments_json"`
	}
	_ = json.Unmarshal(data, &d)

	var args map[string]any
	_ = json.Unmarshal([]byte(d.Args), &args)

	summary := ""
	for _, key := range []string{"command", "path", "file_path", "pattern", "url", "query", "name", "description", "prompt"} {
		if v, ok := args[key].(string); ok && v != "" {
			summary = v
			break
		}
	}
	if summary == "" && len(args) > 0 {
		summary = d.Args
	}

	line := d.Tool
	if summary != "" {
		line += ": " + strings.Join(strings.Fields(summary), " ")
	}
	line = strings.ReplaceAll(line, "`", "'")
	return truncate(line, 160)
}

func titleOf(e indexEntry) string {
	if t := strings.TrimSpace(e.Title); t != "" {
		return t
	}
	return "Untitled session"
}

// fileName places a session by its start date: 2026/11/09-1430-title-id.md.
func fileName(e indexEntry) string {
	started := time.UnixMilli(e.Created)
	return filepath.Join(
		started.Format("2006"),
		started.Format("01"),
		fmt.Sprintf("%s-%s-%s.md", started.Format("02-1504"), slug(e.Title), e.ID),
	)
}

func slug(title string) string {
	var b strings.Builder
	dash := false
	n := 0
	for _, r := range strings.ToLower(title) {
		if n >= 40 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			n++
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
			n++
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "untitled"
	}
	return s
}

func clock(ms int64) string {
	return time.UnixMilli(ms).Format("15:04")
}

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// Summary is one session for the chat's /sessions list.
type Summary struct {
	ID      string
	Title   string
	Updated time.Time
}

// List returns the n most recently updated root sessions, newest first.
func List(sessionsDir string, n int) ([]Summary, error) {
	index, err := readIndex(filepath.Join(sessionsDir, "index.jsonl"))
	if err != nil {
		return nil, err
	}

	var list []Summary
	for id, e := range index {
		if e.Role != "" && e.Role != "root" {
			continue
		}
		list = append(list, Summary{ID: id, Title: titleOf(e), Updated: time.UnixMilli(e.Updated)})
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Updated.After(list[b].Updated) })
	if len(list) > n {
		list = list[:n]
	}
	return list, nil
}
