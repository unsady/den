package seed

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
)

// describeLimit caps a note's description in the index, so one long line
// does not crowd the rules every session reads.
const describeLimit = 120

// The record separator starts each note in listNotes' output.
const separator = "\x1e"

// listNotes prints each note, but those about skills, as the separator, its
// path, and its first bytes. It runs as the agent, so a link the agent put
// among its notes reaches only what the agent may read itself.
const listNotes = `cd "$1" && [ -d notes ] || exit 0
find notes -name '*.md' -type f ! -path 'notes/skills/*' ! -path notes/README.md -exec sh -c 'for f; do printf "\036%s\n" "$f"; head -c 2048 -- "$f"; done' sh {} +`

// notesIndex lists the notes in the space with what each holds, as a
// section for den's rules. It is empty when there are no notes.
func notesIndex(cfg config.Config) (string, error) {
	if cfg.Space == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, err := proc.Command(ctx, cfg, "/bin/sh", "-c", listNotes, "notes", cfg.Space)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("listing notes: %w", err)
	}

	var lines []string
	for _, record := range strings.Split(string(out), separator)[1:] {
		path, head, _ := strings.Cut(record, "\n")
		line := "- " + path
		if about := describe(head); about != "" {
			line += ": " + about
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", nil
	}
	sort.Strings(lines)
	return "\n## Notes\n\nWhat each note in `~/space/notes` holds:\n\n" + strings.Join(lines, "\n") + "\n", nil
}

// describe returns what a note holds: the description in its frontmatter,
// or else its first heading, on one line and cut to describeLimit.
func describe(note string) string {
	var about string
	if rest, ok := strings.CutPrefix(note, "---\n"); ok {
		front, _, _ := strings.Cut(rest, "\n---")
		for line := range strings.SplitSeq(front, "\n") {
			if value, ok := strings.CutPrefix(line, "description:"); ok {
				about = strings.Trim(strings.TrimSpace(value), `"'`)
				break
			}
		}
	}
	if about == "" {
		for line := range strings.SplitSeq(note, "\n") {
			if title, ok := strings.CutPrefix(line, "# "); ok {
				about = title
				break
			}
		}
	}

	about = strings.Join(strings.Fields(about), " ")
	if runes := []rune(about); len(runes) > describeLimit {
		about = strings.TrimSpace(string(runes[:describeLimit])) + "…"
	}
	return about
}
