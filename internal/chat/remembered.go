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

// remembered is what a session's agent left, and when it left the first
// line.
type remembered struct {
	at    time.Time
	lines []string
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
	r, ok := c.remembered[session]
	if !ok {
		r = remembered{at: time.Now()}
	}
	if len(r.lines) >= rememberedLines {
		return fmt.Errorf("this turn already has %d lines; merge them into fewer", rememberedLines)
	}
	r.lines = append(r.lines, line)
	c.remembered[session] = r
	return nil
}

// Remembered returns what the agent in session left during the turn that
// started at since, and forgets it; lines from before since belong to an
// earlier turn and do not count.
func (c *Chat) Remembered(session string, since time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.remembered[session]
	delete(c.remembered, session)
	if !ok || r.at.Before(since) {
		return nil
	}
	return r.lines
}

// MemoryNote is what a reply says of the memory: nil when the turn neither
// changed it nor left a line on it.
func MemoryNote(changed bool, lines []string) *events.Memory {
	if !changed && len(lines) == 0 {
		return nil
	}
	return &events.Memory{Lines: lines}
}
