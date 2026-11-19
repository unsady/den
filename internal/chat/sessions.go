package chat

// The chat's sessions live in den's store. Each client has its own cursor:
// the session its next message goes to, or none when that message starts a
// new one. A turn belongs to the session its messages went to when they
// came, whatever the cursor does meanwhile.

import (
	"context"
	"errors"
	"log"

	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
)

// app is the app's client name for its cursor.
const appClient = "app"

// cursor returns the session client is in, "" when its next message starts
// a new one.
func (c *Chat) cursor(client string) string {
	id, err := c.store.Cursor(context.Background(), client)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("chat: cursor of %s: %v", client, err)
	}
	return id
}

// enter puts client in session, or takes it out of any when session is
// empty, so its next message starts a new one.
func (c *Chat) enter(client, session string) error {
	ctx := context.Background()
	if session == "" {
		return c.store.ClearCursor(ctx, client)
	}
	if err := c.store.SetCursor(ctx, client, session); err != nil {
		return err
	}
	return c.store.Touch(ctx, session)
}

// joined returns the session client is in, and starts a chat session for
// it when it is in none.
func (c *Chat) joined(client string) (string, error) {
	if id := c.cursor(client); id != "" {
		return id, nil
	}

	id := newSession()
	if err := c.store.AddSession(context.Background(), store.Session{ID: id, Kind: store.KindChat}); err != nil {
		return "", err
	}
	return id, c.enter(client, id)
}

// target returns the session a Telegram message goes to: the one that wrote
// the bot's message it replies to, or Telegram's own.
func (c *Chat) target(replyTo int64) (string, error) {
	if replyTo != 0 {
		id, err := c.store.TelegramSession(context.Background(), replyTo)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	return c.joined(store.Telegram)
}

// Sent ties the bot's messages to session, which wrote them, so a reply to
// one goes on there.
func (c *Chat) Sent(ids []int64, session string) {
	if session == "" || len(ids) == 0 {
		return
	}
	if err := c.store.SentTelegram(context.Background(), session, ids...); err != nil {
		log.Printf("chat: sent: %v", err)
	}
}

// icons mark each kind of session above a reply from it.
var icons = map[store.Kind]string{store.KindChat: "↩", store.KindBackground: "⚙", store.KindJob: "⏰"}

// placeholders name a session of each kind that has no title yet.
var placeholders = map[store.Kind]string{store.KindChat: "Earlier session", store.KindBackground: "Background task", store.KindJob: "Job"}

// label names session above a reply in Telegram that comes from a session
// other than Telegram's own, and is empty for Telegram's own.
func (c *Chat) label(session string) string {
	if session == c.cursor(store.Telegram) {
		return ""
	}
	s, err := c.store.Session(context.Background(), session)
	if err != nil {
		return icons[store.KindChat] + " " + placeholders[store.KindChat]
	}
	return icons[s.Kind] + " " + clipRunes(titleOf(s), 40)
}

// titleOf is a session's title, or what names one without it.
func titleOf(s store.Session) string {
	if s.Title != "" {
		return s.Title
	}
	return placeholders[s.Kind]
}

// untitled says whether session is a chat session that has no title yet.
func (c *Chat) untitled(session string) bool {
	s, err := c.store.Session(context.Background(), session)
	return err == nil && s.Kind == store.KindChat && s.Title == ""
}

// choiceOf returns the model a turn in session runs on: the chat's for a
// chat session, and the one that ran a job's or a /bg task's, or the
// chat's when it was fx's own.
func (c *Chat) choiceOf(session string) (runner.Choice, error) {
	s, err := c.store.Session(context.Background(), session)
	if err != nil {
		return runner.Choice{}, err
	}

	if s.Kind == store.KindChat || s.Choice.Provider == "" && s.Choice.Model == "" {
		return c.mainModel(), nil
	}
	return runner.Choice(s.Choice), nil
}

// Sessions lists the sessions for the app, the latest used first.
func (c *Chat) Sessions(ctx context.Context) ([]store.Session, error) {
	return c.store.Sessions(ctx, store.Filter{Limit: 100})
}
