// Package memory reads and changes the sections of the space's AGENTS.md
// that every session loads, for the app to show and edit. den reads and
// writes the file as the agent, since the space is the agent's.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
)

// File is the memory's file in the space.
const File = "AGENTS.md"

// ErrChanged says the file changed since the version the app read.
var ErrChanged = errors.New("the memory changed since it was read")

// Memory is the text of each section and the version of the file it came
// from.
type Memory struct {
	Soul    string `json:"soul"`
	User    string `json:"user"`
	Notes   string `json:"notes"`
	Version string `json:"version"`
}

// names are the sections' headings, in the order the file keeps them.
var names = []string{"Soul", "User", "Notes"}

func (m Memory) texts() []string {
	return []string{m.Soul, m.User, m.Notes}
}

// Read reads the memory. A missing file reads as empty sections.
func Read(cfg config.Config) (Memory, error) {
	data, err := read(cfg)
	if err != nil {
		return Memory{}, err
	}

	text := string(data)
	return Memory{
		Soul:    section(text, "Soul"),
		User:    section(text, "User"),
		Notes:   section(text, "Notes"),
		Version: version(data),
	}, nil
}

// Write puts m's sections into the file, when it is still at m.Version, and
// commits it. Lines outside the three sections stay as they are.
func Write(cfg config.Config, m Memory) error {
	data, err := read(cfg)
	if err != nil {
		return err
	}
	if version(data) != m.Version {
		return ErrChanged
	}

	text := string(data)
	for i, name := range names {
		text = put(text, name, m.texts()[i])
	}
	if text == string(data) {
		return nil
	}

	path := filepath.Join(cfg.Space, File)
	if err := proc.WriteFile(cfg, path, []byte(text), 0o644); err != nil {
		return err
	}
	return commit(cfg)
}

func read(cfg config.Config) ([]byte, error) {
	path := filepath.Join(cfg.Space, File)
	// cat fails the same way for a missing file and any other error, so
	// den asks first, which it may do as root.
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return proc.ReadFile(cfg, path)
}

// version names the file's content; empty for a missing file.
func version(data []byte) string {
	if data == nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// commit commits the file alone, so what else the agent has changed in the
// space stays for its own commit.
func commit(cfg config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	script := `cd "$1" && git add -- "$2" && git commit -q -m "Edit memory in the app" -- "$2"`
	cmd, err := proc.Command(ctx, cfg, "/bin/sh", "-c", script, "commit", cfg.Space, File)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("committing the memory: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// heading says whether line starts a section of the top two levels, which
// ends the section before it.
func heading(line string) bool {
	return strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")
}

// span finds the section name in lines: the index of its heading and of the
// line after its body, or -1 when there is none.
func span(lines []string, name string) (int, int) {
	for i, line := range lines {
		if strings.TrimSpace(line) != "## "+name {
			continue
		}
		end := i + 1
		for end < len(lines) && !heading(lines[end]) {
			end++
		}
		return i, end
	}
	return -1, -1
}

// section returns the body of the section name, trimmed.
func section(text, name string) string {
	lines := strings.Split(text, "\n")
	start, end := span(lines, name)
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[start+1:end], "\n"))
}

// put sets the body of the section name to body. A missing section goes
// before the first of those that follow it in names, or at the end.
func put(text, name, body string) string {
	body = strings.TrimSpace(body)
	lines := strings.Split(text, "\n")
	block := []string{"## " + name, ""}
	if body != "" {
		block = append(block, strings.Split(body, "\n")...)
		block = append(block, "")
	}

	start, end := span(lines, name)
	if start >= 0 {
		return strings.Join(splice(lines, start, end, block), "\n")
	}

	after := false
	for _, next := range names {
		if next == name {
			after = true
			continue
		}
		if !after {
			continue
		}
		if at, _ := span(lines, next); at >= 0 {
			return strings.Join(splice(lines, at, at, block), "\n")
		}
	}

	text = strings.TrimRight(text, "\n")
	if text != "" {
		text += "\n\n"
	}
	return text + strings.Join(block, "\n")
}

func splice(lines []string, start, end int, block []string) []string {
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:start]...)
	out = append(out, block...)
	return append(out, lines[end:]...)
}
