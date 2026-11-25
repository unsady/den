package chat

// The files the agent sends the user, such as charts, go with the reply of
// the turn that sent them, where the reply goes: one message in the app,
// the reply and an album in Telegram.

import (
	"context"
	"encoding/json"
	"log"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
)

// Upload is a file the agent sends the user, with the widget that
// describes it for the app to draw itself, such as a chart's data.
type Upload struct {
	notify.Upload
	Widget json.RawMessage
}

// Send sends the user a file the agent in session sent. While a turn of
// the chat's runs there, on its agent or one of its own, the file waits
// for its reply, so they arrive together. Otherwise it goes now, to
// Telegram and the session.
func (c *Chat) Send(ctx context.Context, session string, f Upload) error {
	c.mu.Lock()
	if files, ok := c.attached[session]; ok {
		c.attached[session] = append(files, f)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if kept := c.keep(session, []Upload{f}); len(kept) > 0 {
		c.Events.Publish(events.Event{Type: events.File, Session: session, Files: kept})
	}

	telegram, err := notify.Load(c.cfg)
	if err != nil {
		return err
	}
	ids, err := telegram.SendFiles(ctx, []notify.Upload{f.Upload})
	c.Sent(ids, session)
	return err
}

// keep saves files with session for the app, and returns what a message
// says of them.
func (c *Chat) keep(session string, files []Upload) []events.Attachment {
	if session == "" || c.Events == nil || c.Events.Dir == "" {
		return nil
	}

	var kept []events.Attachment
	for _, f := range files {
		file, err := c.Events.SaveFile(session, f.Name, f.Data)
		if err != nil {
			log.Printf("chat: keep a file for the app: %v", err)
			continue
		}
		kept = append(kept, events.Attachment{File: file, Name: f.Name, Size: len(f.Data), Caption: f.Caption, Source: f.Source, Widget: f.Widget})
	}
	return kept
}

// uploads are files as Telegram takes them.
func uploads(files []Upload) []notify.Upload {
	out := make([]notify.Upload, len(files))
	for i, f := range files {
		out[i] = f.Upload
	}
	return out
}
