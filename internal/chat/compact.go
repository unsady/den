package chat

import (
	"context"
	"errors"
	"fmt"
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

// compact compacts the chat's session when the user has been away and it
// is long enough to be worth it. The turn's reply is not sent.
func (c *Chat) compact(ctx context.Context) {
	c.mu.Lock()
	session, used, waiting := c.state.Session, c.state.Context, len(c.inbox) > 0
	c.mu.Unlock()
	if session == "" || waiting || used < compactAbove {
		return
	}
	if window := c.currentWindow(ctx); window == 0 || used*100/window < compactPercent {
		return
	}
	if _, err := c.squeezeSession(ctx); err != nil {
		log.Printf("chat: compacting failed: %v", err)
	}
}

// compactNow compacts the session at the user's request, for /compact, and
// says how it went.
func (c *Chat) compactNow(ctx context.Context) string {
	c.mu.Lock()
	session, used := c.state.Session, c.state.Context
	c.mu.Unlock()
	if session == "" {
		return "There is no session to compact yet."
	}
	// fx compacts from compactPercent of the window, so a smaller session
	// would come back as it was.
	if window := c.currentWindow(ctx); window > 0 && used > 0 && used*100/window < compactPercent {
		return fmt.Sprintf("Context is %s of %s: fx compacts a session from %d%% of the window, so this one stays as it is.",
			tokens(used), tokens(window), compactPercent)
	}

	after, err := c.squeezeSession(ctx)
	if err == nil {
		// The user asked for this one, so the next reply need not tell them.
		c.mu.Lock()
		c.state.Compacted = 0
		c.save()
		c.mu.Unlock()
	}
	switch {
	case err != nil:
		return "Compacting failed: " + err.Error()
	case after == 0:
		return "🗜 Compacted the session."
	case used > 0 && after >= used:
		return fmt.Sprintf("fx left the session as it was: %s tokens.", tokens(after))
	case used > 0:
		return fmt.Sprintf("🗜 Compacted the session: %s → %s tokens.", tokens(used), tokens(after))
	}
	return fmt.Sprintf("🗜 Compacted the session: now %s tokens.", tokens(after))
}

// currentWindow is the context window of the chat's model, 0 when unknown.
func (c *Chat) currentWindow(ctx context.Context) int {
	c.mu.Lock()
	provider, model := c.state.Provider, c.state.Model
	c.mu.Unlock()
	setup, err := runner.Status(ctx, c.cfg, provider)
	if err != nil {
		return 0
	}
	if model == "" {
		model = setup.Model
	}
	return c.window(setup.Provider, model)
}

// squeezeSession runs the turn that makes fx compact the session, and
// returns the session's size after it, 0 when unknown.
func (c *Chat) squeezeSession(ctx context.Context) (int, error) {
	c.mu.Lock()
	session, used := c.state.Session, c.state.Context
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	generation := c.generation
	if len(c.inbox) > 0 {
		c.mu.Unlock()
		return 0, errors.New("a message is waiting")
	}
	turn, stop := context.WithCancel(ctx)
	c.stop = stop
	c.mu.Unlock()
	defer stop()

	log.Printf("chat: compacting session %s at %d tokens", session, used)
	started := time.Now()
	reply := runner.Ask(turn, c.cfg, runner.Request{
		Prompt:         compactPrompt,
		Provider:       provider,
		Model:          model,
		Effort:         effort,
		Resume:         session,
		CompactPercent: compactPercent,
	})
	after := 0
	if c.den != nil {
		after = c.den.LastContext(started)
	}

	c.mu.Lock()
	c.stop = nil
	if generation == c.generation && reply.Error == "" && reply.SessionID != "" {
		c.state.Session = reply.SessionID
		c.state.Context = after
		// The next reply tells the user about the compaction.
		c.state.Compacted = used
		c.save()
	}
	c.mu.Unlock()

	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	if reply.Error != "" {
		return 0, errors.New(reply.Error)
	}
	log.Printf("chat: compacted session %s in %s", session, reply.Duration)
	return after, nil
}
