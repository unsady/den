package chat

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/store"
)

// What the user can do when a turn fails, and stopping one session.

// carryOn is what den tells the agent when the user asks it to go on with
// a turn that broke off.
const carryOn = "[den: your last turn broke off with an error. Carry on from where it stopped.]"

// ErrNothingHeld says a session holds no messages to send again.
var ErrNothingHeld = errors.New("nothing to retry in this session")

// recovery decides what the user can do about session's failed turn, which
// ran messages. A turn whose agent took no step did nothing, so den holds
// its messages to send again; after a step, the agent goes on instead.
func (c *Chat) recovery(session string, messages []store.Message, stepped bool) string {
	if stepped {
		// What an earlier turn held is stale once the agent went on.
		if err := c.store.DropHeld(context.Background(), session); err != nil {
			log.Printf("chat: held %s: %v", session, err)
		}
		return events.RetryContinue
	}
	if err := c.store.Hold(context.Background(), session, messages); err != nil {
		log.Printf("chat: hold %s: %v", session, err)
		return ""
	}
	return events.RetryAgain
}

// Retry sends the messages of session's failed turn again.
func (c *Chat) Retry(ctx context.Context, session string) error {
	released, err := c.store.Release(ctx, session)
	if err != nil {
		return err
	}
	if !released {
		return ErrNothingHeld
	}
	c.poke()
	return nil
}

// Holds says whether session holds the messages of a failed turn to send
// again.
func (c *Chat) Holds(ctx context.Context, session string) bool {
	held, err := c.store.Holds(ctx, session)
	if err != nil {
		log.Printf("chat: held %s: %v", session, err)
	}
	return held
}

// Continue asks the agent to go on in session after its turn broke off.
func (c *Chat) Continue(ctx context.Context, session string) error {
	if _, err := c.store.Session(ctx, session); err != nil {
		return err
	}
	e := []entry{{text: carryOn, report: true, session: session}}
	if c.steer(ctx, e) {
		return nil
	}
	return c.enqueue(e...)
}

// wroteOn forgets what the session of the user's entries held to retry:
// the user wrote on instead.
func (c *Chat) wroteOn(entries []entry) {
	for _, e := range entries {
		if e.report || e.session == "" {
			continue
		}
		if err := c.store.DropHeld(context.Background(), e.session); err != nil {
			log.Printf("chat: held %s: %v", e.session, err)
		}
		return
	}
}

// StopSession stops session's running turn and drops what waits for it,
// and says whether a turn ran. Other sessions go on.
func (c *Chat) StopSession(ctx context.Context, session string) bool {
	dropped, err := c.store.ClearInbox(context.Background(), session)
	if err != nil {
		log.Printf("chat: inbox: %v", err)
	}
	var notices []string
	for _, m := range dropped {
		if m.Report && m.Notice != "" {
			notices = append(notices, m.Notice)
		}
	}

	c.mu.Lock()
	stop := c.turns[session]
	for key, st := range c.steered {
		if st.session == session {
			delete(c.steered, key)
		}
	}
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	c.tell(ctx, nil, notices)
	return stop != nil
}

// Settle waits until the turn StopSession stopped in session has ended and
// let the session go, or until ctx ends. A turn leaves the running ones
// before it sends its reply, and holds the session until it has sent it, so
// Forget refuses the session till then.
func (c *Chat) Settle(ctx context.Context, session string) error {
	for {
		c.mu.Lock()
		running := c.turns[session] != nil || c.busy[session] > 0
		c.mu.Unlock()
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// sendFailure sends a reply to Telegram, with the button that retries or
// goes on under it when retry says the turn failed.
func (c *Chat) sendFailure(ctx context.Context, telegram *notify.Telegram, text, session, retry string) ([]int64, error) {
	var button notify.Button
	switch retry {
	case events.RetryAgain:
		button = notify.Button{Text: "↻ Retry", Data: "rt:" + session}
	case events.RetryContinue:
		button = notify.Button{Text: "→ Continue", Data: "ct:" + session}
	default:
		return telegram.SendIDs(ctx, text)
	}
	id, err := telegram.SendButtons(ctx, text, [][]notify.Button{{button}})
	if err != nil {
		return nil, err
	}
	return []int64{id}, nil
}
