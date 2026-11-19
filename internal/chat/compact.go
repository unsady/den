package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/unsady/den/internal/acp"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
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

// compactShare is the smallest share of the model's window, in percent, at
// which den compacts a session while the user is away.
const compactShare = 10

// compact compacts the chat's session when the user has been away and it
// is long enough to be worth it.
func (c *Chat) compact(ctx context.Context) {
	used := c.sizeOf(c.cursor(store.Telegram))
	if c.waiting() || used < compactAbove {
		return
	}
	// A model with a large window keeps the details a while longer.
	if window := c.currentWindow(ctx); window == 0 || used*100/window < compactShare {
		return
	}
	if _, err := c.squeezeSession(ctx, false); err != nil {
		log.Printf("chat: compacting failed: %v", err)
	}
}

// compactNow compacts the session at the user's request, for /compact, and
// says how it went.
func (c *Chat) compactNow(ctx context.Context) string {
	if !c.saved(c.cursor(store.Telegram)) {
		return "There is no session to compact yet."
	}

	// It takes a while, so the chat shows den typing meanwhile.
	done := make(chan struct{})
	go c.showTyping(ctx, done)
	out, err := c.squeezeSession(ctx, true)
	close(done)
	switch {
	case err != nil:
		return "Compacting failed: " + err.Error()
	case !out.Compacted:
		return "fx found nothing to compact in this session."
	}
	return fmt.Sprintf("🗜 Compacted the session: %s → %s tokens.", tokens(out.Before), tokens(out.After))
}

// compacted records that fx compacted session, from used tokens to after.
// fx compacts a session in place, under its id.
func (c *Chat) compacted(session string, used, after int, quiet bool) {
	ctx := context.Background()
	if err := c.store.SetContext(ctx, session, after); err != nil {
		log.Printf("chat: compacted session %s: %v", session, err)
	}
	if quiet {
		used = 0
	}
	if err := c.store.SetCompacted(ctx, session, used); err != nil {
		log.Printf("chat: compacted session %s: %v", session, err)
	}
}

// currentWindow is the context window of the chat's model, 0 when unknown.
func (c *Chat) currentWindow(ctx context.Context) int {
	m := c.mainModel()
	setup, err := runner.Status(ctx, c.cfg, m.Provider)
	if err != nil {
		return 0
	}
	model := m.Model
	if model == "" {
		model = setup.Model
	}
	return c.window(setup.Provider, model)
}

// sizeOf is session's size after its last turn, in tokens, 0 when unknown.
func (c *Chat) sizeOf(session string) int {
	if session == "" {
		return 0
	}
	s, err := c.store.Session(context.Background(), session)
	if err != nil {
		return 0
	}
	return s.Context
}

// squeezeSession has fx compact the chat's session in place, and returns
// what came of it. The next reply in the session tells the user, unless
// they asked for it, as quiet says.
func (c *Chat) squeezeSession(ctx context.Context, quiet bool) (acp.Compaction, error) {
	session := c.cursor(store.Telegram)
	if session == "" {
		return acp.Compaction{}, errors.New("no session")
	}
	if c.waiting() {
		return acp.Compaction{}, errors.New("a message is waiting")
	}
	m := c.mainModel()
	c.mu.Lock()
	turn, stop := context.WithCancel(ctx)
	c.stop = stop
	c.mu.Unlock()
	defer stop()
	release := c.Occupy(session)
	defer release()

	log.Printf("chat: compacting session %s", session)
	started := time.Now()
	out, err := c.agent.Compact(turn, runner.Request{Provider: m.Provider, Model: m.Model, Effort: m.Effort, Resume: session})

	c.mu.Lock()
	c.stop = nil
	c.mu.Unlock()

	if err != nil {
		return out, err
	}
	if out.Compacted {
		c.compacted(session, out.Before, out.After, quiet)
	}
	log.Printf("chat: compacted session %s in %s: %d → %d tokens", session, time.Since(started).Round(time.Second), out.Before, out.After)
	return out, nil
}
