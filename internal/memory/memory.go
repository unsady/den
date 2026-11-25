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
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

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

// writing holds one Write at a time.
var writing sync.Mutex

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
// commits it when it can. Lines outside the three sections stay as they are.
func Write(cfg config.Config, m Memory) error {
	// Two saves at once would both pass the version check.
	writing.Lock()
	defer writing.Unlock()

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

	// The memory is saved once written: a commit that fails, as on the
	// agent's own git holding the index, leaves the change for the agent's
	// next commit, and failing the save would only send the app a 409 on
	// its retry.
	if err := commit(cfg); err != nil {
		log.Printf("memory: %v", err)
	}
	return nil
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

	// The file can match the last commit again, when the edit undoes the
	// agent's uncommitted one, and then there is nothing to commit.
	script := `cd "$1" && git add -- "$2" && { git diff --cached --quiet -- "$2" || git commit -q -m "Edit memory in the app" -- "$2"; }`
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
		// A line such as a shell comment in a code block is no heading.
		end, fenced := i+1, false
		for ; end < len(lines); end++ {
			trimmed := strings.TrimSpace(lines[end])
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				fenced = !fenced
			}
			if !fenced && heading(lines[end]) {
				break
			}
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
	return trim(strings.Join(lines[start+1:end], "\n"))
}

// trim drops the blank lines around text and the spaces at its end, but
// keeps the indent of its first line, such as a nested item's.
func trim(text string) string {
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	for {
		line, rest, found := strings.Cut(text, "\n")
		if !found || strings.TrimSpace(line) != "" {
			break
		}
		text = rest
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// demote makes a heading of the top two levels in body, outside code
// blocks, one of the third, so it stays in the section rather than end it.
func demote(body string) string {
	lines := strings.Split(body, "\n")
	fenced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		}
		if !fenced && heading(line) {
			lines[i] = "### " + strings.TrimLeft(line, "# ")
		}
	}
	return strings.Join(lines, "\n")
}

// put sets the body of the section name to body. A missing section goes
// before the first of those that follow it in names, or at the end.
func put(text, name, body string) string {
	body = demote(trim(body))
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

// missing is the stamp of a missing file, so creating it counts as a change.
const missing = "missing"

// Stamp names the file's content as it is now, so a caller can tell
// whether a turn changed it; empty when it cannot be read.
func Stamp(cfg config.Config) string {
	data, err := read(cfg)
	if err != nil {
		log.Printf("memory: %v", err)
		return ""
	}
	if data == nil {
		return missing
	}
	return version(data)
}

// Changed says whether the file changed since it had the stamp before. A
// stamp den could not read tells nothing.
func Changed(cfg config.Config, before string) bool {
	if before == "" {
		return false
	}
	after := Stamp(cfg)
	return after != "" && after != before
}
