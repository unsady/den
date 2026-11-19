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
	m := c.mainModel()
	// The task's session is in the store before it runs, so what its
	// commands report and the replies to its messages wait there until it
	// ends.
	session := newSession()
	err := c.store.AddSession(context.Background(), store.Session{ID: session, Kind: store.KindBackground, Parent: parent, Choice: store.Choice(m)})
	if err != nil {
		cancel()
		log.Printf("chat: background session: %v", err)
		c.tell(ctx, telegram, []string{"den could not start the task: " + err.Error()})
		return
	}
	release := c.Occupy(session)

	c.mu.Lock()
	c.bg[id] = cancel
	c.mu.Unlock()
	const after = " Its answer comes here; reply to it to go on there."
	var started int64
	if !app {
		call, done := context.WithTimeout(ctx, time.Minute)
		started, err = telegram.Post(call, "⚙ Background task started."+after)
		done()
		if err != nil {
			log.Printf("chat: send: %v", err)
		} else {
			c.Sent([]int64{started}, session)
		}
	}

	// A model titles the task while it runs, and the message says the
	// title once it has it.
	titled := make(chan struct{})
	c.background.Go(func() {
		defer close(titled)
		title := c.makeTitle(run, task)
		if title == "" {
			return
		}
		if _, err := c.store.SetTitle(context.Background(), session, title); err != nil {
			log.Printf("chat: background title: %v", err)
			return
		}
		c.Events.Publish(events.Event{Type: events.Titled, Session: session, Title: title})
		if started != 0 {
			c.editPlain(ctx, telegram, started, icons[store.KindBackground]+" "+title+" runs in the background."+after)
		}
	})

	c.background.Go(func() {
		defer cancel()
		defer release()
		agent := runner.NewAgent(c.cfg)
		reply := agent.Ask(run, runner.Request{
			Prompt:   task,
			Provider: m.Provider,
			Model:    m.Model,
			Effort:   m.Effort,
			Resume:   session,
		})
		agent.Close()
		log.Printf("chat: background turn in %s", reply.Duration)
		if reply.InputTokens > 0 || reply.OutputTokens > 0 {
			c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
		}
		if err := c.store.Touch(context.Background(), session); err != nil {
			log.Printf("chat: touch %s: %v", session, err)
		}

		c.mu.Lock()
		delete(c.bg, id)
		c.mu.Unlock()
		if run.Err() == context.Canceled {
			// /stop already answered, or den is shutting down.
			return
		}

		<-titled
		s, err := c.store.Session(context.Background(), session)
		if err != nil {
			log.Printf("chat: background session: %v", err)
			return
		}
		c.Events.Publish(events.Event{Type: events.Message, At: asked, Session: session, Title: s.Title, Kind: string(store.KindBackground), Text: task})
		c.Events.Publish(events.Event{Type: events.Reply, Session: session, Title: s.Title, Kind: string(store.KindBackground), Text: answer(reply, false), Failed: reply.Error != ""})
		if app {
			return
		}
		telegram, err := notify.Load(c.cfg)
		if err != nil {
			return
		}
		call, done := context.WithTimeout(ctx, time.Minute)
		heading := icons[store.KindBackground] + " " + titleOf(s)
		ids, err := telegram.SendIDs(call, heading+"\n\n"+answer(reply, false))
		done()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
		c.Sent(ids, session)
	})
}
