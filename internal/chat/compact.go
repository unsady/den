package chat

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/unsady/den/internal/acp"
	"github.com/unsady/den/internal/events"
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

// compact compacts the chat's session when the user has been away and it
// is long enough to be worth it.
func (c *Chat) compact(ctx context.Context) {
	session := c.cursor(store.Telegram)
	if c.waiting() || c.agentBusy() || c.sizeOf(session) < compactAbove {
		return
	}
	if _, err := c.squeezeSession(ctx, session, false); err != nil {
		log.Printf("chat: compacting failed: %v", err)
	}
}

// compactNow compacts the chat's session at the user's request, for
// /compact, and says how it went.
func (c *Chat) compactNow(ctx context.Context) string {
	session := c.cursor(store.Telegram)
	if !c.saved(session) {
		return "There is no session to compact yet."
	}

	// It takes a while, so the chat shows den typing meanwhile.
	done := make(chan struct{})
	go c.showTyping(ctx, done)
	out, err := c.squeezeSession(ctx, session, true)
	close(done)
	switch {
	case err != nil:
		return "Compacting failed: " + err.Error()
	case !out.Compacted:
		return "fx found nothing to compact in this session."
	}
	return fmt.Sprintf("🗜 Compacted the session: %s → %s tokens.", tokens(out.Before), tokens(out.After))
}

// Compact starts compacting session for the app, which hears how it went
// from the session's events. It refuses while a turn, a job or a /bg task
// runs in the session.
func (c *Chat) Compact(ctx context.Context, session string) error {
	if !c.saved(session) {
		return ErrUnknown
	}
	c.mu.Lock()
	busy, life := c.busy[session] > 0 || c.stopping, c.life
	c.mu.Unlock()
	if busy {
		return ErrBusy
	}

	c.background.Go(func() {
		if _, err := c.squeezeSession(life, session, true); err != nil {
			log.Printf("chat: compacting %s failed: %v", session, err)
		}
	})
	return nil
}

// ErrUnknown is Compact's error for a session fx has not saved.
var ErrUnknown = errors.New("no such session")

// compacted records that fx compacted session, from used tokens to after,
// and puts the mark in the session's feed. fx compacts a session in place,
// under its id. Unless quiet, the session's next reply in Telegram says it.
func (c *Chat) compacted(session string, used, after int, quiet bool) {
	ctx := context.Background()
	if err := c.store.SetContext(ctx, session, after); err != nil {
		log.Printf("chat: compacted session %s: %v", session, err)
	}
	c.Events.Publish(events.Event{Type: events.Compacted, Session: session, Before: used, Context: after})

	if quiet {
		used = 0
	}
	if err := c.store.SetCompacted(ctx, session, used); err != nil {
		log.Printf("chat: compacted session %s: %v", session, err)
	}
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

// squeezeSession has fx compact session in place, on the session's model,
// and returns what came of it. The app sees it start and end; the next
// reply in Telegram tells the user, unless they asked for it, as quiet
// says.
func (c *Chat) squeezeSession(ctx context.Context, session string, quiet bool) (out acp.Compaction, err error) {
	if session == "" {
		return out, errors.New("no session")
	}
	choice, err := c.choiceOf(session)
	if err != nil {
		return out, err
	}

	// The session is den's alone while it compacts. It compacts on the
	// chat's agent when that is free, which drops the session it held so
	// no fx keeps an older copy of it; else on an agent of its own, as the
	// chat's agent then holds another session.
	c.mu.Lock()
	if c.busy[session] > 0 {
		c.mu.Unlock()
		return out, errors.New("a turn runs")
	}
	turn, stop := context.WithCancel(ctx)
	agent, id := c.agent, ""
	if c.stop == nil {
		c.stop = stop
	} else {
		agent, id = runner.NewAgent(c.cfg), rand.Text()
		c.bg[id] = stop
	}
	release := c.occupy(session)
	c.mu.Unlock()
	defer stop()
	defer release()
	defer func() {
		c.mu.Lock()
		if id == "" {
			c.stop = nil
		} else {
			delete(c.bg, id)
		}
		c.mu.Unlock()
		if id != "" {
			agent.Close()
		}

		// Whatever came of it, the app stops showing it run; one that
		// compacted heard Compacted.
		if err != nil || !out.Compacted {
			e := events.Event{Type: events.Context, Session: session, Context: c.sizeOf(session)}
			if err != nil {
				e.Failed, e.Text = true, "Compacting failed: "+err.Error()
			}
			c.Events.Publish(e)
		}
	}()

	c.Events.Publish(events.Event{Type: events.Compacting, Session: session})
	log.Printf("chat: compacting session %s", session)
	started := time.Now()
	out, err = agent.Compact(turn, runner.Request{Provider: choice.Provider, Model: choice.Model, Effort: choice.Effort, Resume: session})
	if err != nil {
		return out, err
	}
	if out.Compacted {
		c.compacted(session, out.Before, out.After, quiet)
	}
	log.Printf("chat: compacted session %s in %s: %d → %d tokens", session, time.Since(started).Round(time.Second), out.Before, out.After)
	return out, nil
}
