package chat

import (
	"context"
	"log"
	"time"

	"github.com/unsady/den/internal/runner"
)

// A provider keeps a prompt's start cached for a while, often an hour, and
// charges much less for the cached part. A long session the user comes back
// to after that is read in full again on every turn, so den compacts it
// while it is still cached: the summary costs little then, and every later
// turn starts small.
var (
	// compactIdle is how long the user is away before den compacts.
	compactIdle = 50 * time.Minute
	// compactAbove is the smallest session worth compacting, in tokens:
	// below it, reading the session again costs less than the details a
	// summary loses.
	compactAbove = 60_000
)

// compactPercent is the lowest share of the model's window at which fx
// compacts, so a model with a large window waits past compactAbove.
const compactPercent = 10

// compactPrompt is the turn that carries the compaction. fx compacts the
// session before it sends it.
const compactPrompt = "[den: the user has been away for a while, so den compacted this session. " +
	"Nothing needs doing now. Reply with exactly " + runner.Silent + ".]"

// compact compacts the chat's session when it is long enough to be worth
// it. The turn's reply is not sent.
func (c *Chat) compact(ctx context.Context) {
	c.mu.Lock()
	session, used := c.state.Session, c.state.Context
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	generation := c.generation
	waiting := len(c.inbox) > 0
	c.mu.Unlock()
	if session == "" || waiting || used < compactAbove {
		return
	}

	setup, err := runner.Status(ctx, c.cfg, provider)
	if err != nil {
		return
	}
	current := model
	if current == "" {
		current = setup.Model
	}
	window := c.window(setup.Provider, current)
	if window == 0 || used*100/window < compactPercent {
		return
	}

	c.mu.Lock()
	if len(c.inbox) > 0 || generation != c.generation {
		c.mu.Unlock()
		return
	}
	turn, stop := context.WithCancel(ctx)
	c.stop = stop
	c.mu.Unlock()
	defer stop()

	log.Printf("chat: compacting session %s at %d tokens", session, used)
	reply := runner.Ask(turn, c.cfg, runner.Request{
		Prompt:         compactPrompt,
		Provider:       provider,
		Model:          model,
		Effort:         effort,
		Resume:         session,
		CompactPercent: compactPercent,
	})

	c.mu.Lock()
	c.stop = nil
	if generation == c.generation && reply.Error == "" && reply.SessionID != "" {
		c.state.Session = reply.SessionID
		// The size after compaction is known after the next reply, which
		// tells the user about it.
		c.state.Context = 0
		c.state.Compacted = used
		c.save()
	}
	c.mu.Unlock()

	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	if reply.Error != "" {
		log.Printf("chat: compacting failed: %s", reply.Error)
		return
	}
	log.Printf("chat: compacted session %s in %s", session, reply.Duration)
}
