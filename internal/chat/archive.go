package chat

import (
	"context"
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

	c.Events.Publish(events.Event{Type: events.Archived, Session: session, Archived: archived})
	return nil
}

// ArchiveIdle puts the sessions unused for ArchiveAfter in the archive,
// except those that wait on something: a running turn, or a question in
// waiting.
func (c *Chat) ArchiveIdle(ctx context.Context, waiting []string) error {
	c.mu.Lock()
	keep := slices.Clone(waiting)
	for session := range c.busy {
		keep = append(keep, session)
	}
	c.mu.Unlock()

	ids, err := c.store.ArchiveIdle(ctx, time.Now().Add(-ArchiveAfter), keep)
	for _, id := range ids {
		c.Events.Publish(events.Event{Type: events.Archived, Session: id, Archived: true})
	}
	return err
}
