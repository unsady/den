package chat

// The lines the agent leaves on what it changed in its memory, which the
// apps show under the reply of the turn that changed it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/unsady/den/internal/events"
)

// The size of what the agent leaves: a few short lines, so the apps show
// them whole.
const (
	rememberedWords = 12
	rememberedLines = 3
)

// remembered is a line the agent left, and when.
type remembered struct {
	at   time.Time
	line string
}

// Remember keeps line as one of what the agent in session changed in its
// memory, for the reply of its running turn. Like Quiet, it trusts the
// session the agent names.
func (c *Chat) Remember(session, line string) error {
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return fmt.Errorf("the line is empty")
	}
	if n := len(strings.Fields(line)); n > rememberedWords {
		return fmt.Errorf("the line has %d words; keep it to %d", n, rememberedWords)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	held := c.remembered[session]
	if len(held) >= rememberedLines {
		return fmt.Errorf("this turn already has %d lines; merge them into fewer", rememberedLines)
	}
	c.remembered[session] = append(held, remembered{at: time.Now(), line: line})
	return nil
}

// Remembered returns what the agent in session left during the turn that
// started at since, and forgets all it left; lines from before since belong
// to an earlier turn, such as a command that outlived it, and do not count.
func (c *Chat) Remembered(session string, since time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lines []string
	for _, r := range c.remembered[session] {
		if !r.at.Before(since) {
			lines = append(lines, r.line)
		}
	}
	delete(c.remembered, session)
	return lines
}

// MemoryNote is what a reply says of the memory: nil when the turn neither
// changed it nor left a line on it.
func MemoryNote(changed bool, lines []string) *events.Memory {
	if !changed && len(lines) == 0 {
		return nil
	}
	return &events.Memory{Lines: lines}
}
