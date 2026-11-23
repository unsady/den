package chat

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/unsady/den/internal/events"
)

// ArchiveAfter is how long a session goes unused before ArchiveIdle puts
// it in the archive.
const ArchiveAfter = 72 * time.Hour

// Archive puts session in the archive, out of the apps' feed and lists, or
// takes it out. The session is kept as it is, and a new message in it
// takes it out again.
func (c *Chat) Archive(ctx context.Context, session string, archived bool) error {
	if err := c.store.SetArchived(ctx, session, archived); err != nil {
		return err
	}

	c.announce(ctx, session, archived)
	return nil
}

// announce tells the apps that session went into the archive or out of
// it, with what an app that does not hold it needs to list it.
func (c *Chat) announce(ctx context.Context, session string, archived bool) {
	e := events.Event{Type: events.Archived, Session: session, Archived: archived}
	if s, err := c.store.Session(ctx, session); err == nil {
		e.Title, e.Kind, e.Job = s.Title, string(s.Kind), s.Job
	}
	c.Events.Publish(e)
}

// touch marks session as used now, and tells the apps when that took it
// out of the archive.
func (c *Chat) touch(ctx context.Context, session string) error {
	restored, err := c.store.Touch(ctx, session)
	if restored {
		c.announce(ctx, session, false)
	}
	return err
}

// restore takes session out of the archive as a message comes for it, so
// it is not archived meanwhile, and the apps list it again.
func (c *Chat) restore(ctx context.Context, session string) {
	restored, err := c.store.Restore(ctx, session)
	if err != nil {
		log.Printf("chat: restore %s: %v", session, err)
	}
	if restored {
		c.announce(ctx, session, false)
	}
}

// ArchiveIdle puts the sessions unused for ArchiveAfter in the archive,
// except those that wait on something: a running turn, a message in the
// inbox, or what waiting names, such as a question or a den map run.
func (c *Chat) ArchiveIdle(ctx context.Context, waiting []string) error {
	// A turn that starts meanwhile waits for the lock, so its session is
	// kept or comes back.
	c.mu.Lock()
	keep := slices.Clone(waiting)
	for session := range c.busy {
		keep = append(keep, session)
	}
	for session := range c.steering {
		keep = append(keep, session)
	}
	ids, err := c.store.ArchiveIdle(ctx, time.Now().Add(-ArchiveAfter), keep)
	c.mu.Unlock()

	for _, id := range ids {
		c.Events.Publish(events.Event{Type: events.Archived, Session: id, Archived: true})
	}
	return err
}
