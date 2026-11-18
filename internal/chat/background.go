package chat

import (
	"context"
	"crypto/rand"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
)

// bgTask returns the task of a /bg message, which may take several lines,
// and whether the message is one.
func bgTask(text string) (string, bool) {
	text = strings.TrimSpace(text)
	end := strings.IndexFunc(text, unicode.IsSpace)
	if end < 0 {
		end = len(text)
	}
	name, _, _ := strings.Cut(text[:end], "@")
	if strings.ToLower(name) != "/bg" {
		return "", false
	}
	return strings.TrimSpace(text[end:]), true
}

// runBackground runs task in a new session of its own besides the chat's
// turns. Its answer comes headed with the task's name, and a reply to it
// goes on in its session. With no telegram, the task is the app's: nothing
// is said in Telegram, and the answer comes only in the app.
func (c *Chat) runBackground(ctx context.Context, telegram *notify.Telegram, task string) {
	if task == "" {
		const hint = "Send /bg with a task, such as: /bg compare these two models"
		if telegram == nil {
			c.tell(ctx, nil, []string{hint})
		} else {
			c.send(ctx, telegram, hint)
		}
		return
	}
	app := telegram == nil
	id := rand.Text()
	asked := time.Now()
	run, cancel := context.WithCancel(ctx)

	parent := c.cursor(store.Telegram)
	if app {
		parent = c.cursor(appClient)
	}
	c.mu.Lock()
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	c.bg[id] = cancel
	c.turns = append(c.turns, turnRef{id: id, bg: true, app: app})
	if len(c.turns) > keptTurns {
		c.turns = c.turns[len(c.turns)-keptTurns:]
	}
	c.mu.Unlock()
	const after = " Its answer comes here; reply to it to go on there."
	var started int64
	if !app {
		call, done := context.WithTimeout(ctx, time.Minute)
		var err error
		started, err = telegram.Post(call, "⚙ Background task started."+after)
		done()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
	}

	// A model titles the task while it runs, and the message says the
	// title once it has it.
	titled := make(chan string, 1)
	c.background.Go(func() {
		title := c.makeTitle(run, task)
		titled <- title
		if started != 0 && title != "" {
			c.editPlain(ctx, telegram, started, icons[store.Background]+" "+title+" runs in the background."+after)
		}
	})

	c.background.Go(func() {
		defer cancel()
		reply := runner.Ask(run, c.cfg, runner.Request{
			Prompt:   task,
			Provider: provider,
			Model:    model,
			Effort:   effort,
			Turn:     id,
		})
		log.Printf("chat: background turn in %s", reply.Duration)
		if reply.InputTokens > 0 || reply.OutputTokens > 0 {
			c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
		}

		// The task's session is known now.
		title := <-titled
		if reply.SessionID != "" {
			err := c.store.AddSession(context.Background(), store.Session{
				ID: reply.SessionID, Kind: store.Background, Parent: parent, Title: title, Created: asked,
				Choice: store.Choice{Provider: provider, Model: model, Effort: effort},
			})
			if err != nil {
				log.Printf("chat: background session: %v", err)
			}
		}

		// Reports that came while the turn ran go to its session, or to the
		// user when there is none.
		c.mu.Lock()
		delete(c.bg, id)
		var pending []entry
		var held []string
		for i := range c.turns {
			if c.turns[i].id == id {
				c.turns[i].session = reply.SessionID
				pending, c.turns[i].pending = c.turns[i].pending, nil
				held, c.turns[i].held = c.turns[i].held, nil
			}
		}
		var notices []string
		for _, e := range pending {
			if reply.SessionID == "" {
				if e.notice != "" {
					notices = append(notices, e.notice)
				}
				continue
			}
			e.session = reply.SessionID
			c.inbox = append(c.inbox, e)
		}
		c.save()
		c.mu.Unlock()
		if len(pending) > len(notices) {
			c.poke()
		}

		if run.Err() == context.Canceled {
			// /stop already answered, or den is shutting down.
			c.publishHeld(reply.SessionID, held)
			return
		}
		if reply.SessionID != "" {
			c.Events.Publish(events.Event{Type: events.Message, At: asked, Session: reply.SessionID, Title: title, Text: task})
		}
		// The notices came while the task ran: after it, before its answer.
		c.publishHeld(reply.SessionID, held)
		if reply.SessionID != "" {
			c.Events.Publish(events.Event{Type: events.Reply, Session: reply.SessionID, Title: title, Text: answer(reply, false), Failed: reply.Error != ""})
		}
		if app {
			c.tell(ctx, nil, notices)
			return
		}
		telegram, err := notify.Load(c.cfg)
		if err != nil {
			return
		}
		c.tell(ctx, telegram, notices)
		call, done := context.WithTimeout(ctx, time.Minute)
		heading := icons[store.Background] + " " + titleOf(store.Session{Kind: store.Background, Title: title})
		ids, err := telegram.SendIDs(call, heading+"\n\n"+answer(reply, false))
		done()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
		c.Sent(ids, reply.SessionID)
	})
}

// publishHeld gives the app the notices a /bg turn held, in session, or in
// none when the turn left no session.
func (c *Chat) publishHeld(session string, held []string) {
	for _, text := range held {
		c.Events.Publish(events.Event{Type: events.Notice, Session: session, Text: text})
	}
}
