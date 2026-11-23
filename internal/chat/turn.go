package chat

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/sessions"
	"github.com/unsady/den/internal/store"
)

// work starts a turn for each session that has messages waiting and no
// turn running, so sessions do not wait for each other, and compacts the
// chat's session once the user has been away for a while.
func (c *Chat) work(ctx context.Context) {
	var idle <-chan time.Time
	// later is set when /compact came while the chat's agent was busy.
	later := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-c.ended:
			idle = time.After(compactIdle)
			if later && !c.isStopping() {
				later = false
				c.background.Go(func() { c.squeezeNow(ctx) })
			}
		case <-idle:
			idle = nil
			if !c.isStopping() {
				c.background.Go(func() { c.compact(ctx) })
			}
		case <-c.squeeze:
			if c.isStopping() {
				break
			}
			if c.agentBusy() {
				later = true
				break
			}
			c.background.Go(func() { c.squeezeNow(ctx) })
		}

		for ctx.Err() == nil && c.turn(ctx) {
		}
	}
}

// squeezeNow compacts the chat's session for /compact and tells the user.
func (c *Chat) squeezeNow(ctx context.Context) {
	if telegram, err := notify.Load(c.cfg); err == nil {
		c.send(ctx, telegram, c.compactNow(ctx))
	}
}

// agentBusy says whether the chat's agent runs a turn or compacts.
func (c *Chat) agentBusy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stop != nil
}

// turn starts a turn for the waiting messages of one session that has no
// turn running. It returns false when there were none.
//
// A turn in the session a client is in runs on the chat's agent when it is
// free. Any other turn, such as a reply to a job's message or one the app
// asked for, runs on an agent of its own, as /bg does. A message sent to
// the session meanwhile joins the turn either way.
func (c *Chat) turn(ctx context.Context) bool {
	// The session is read before its messages leave the inbox, so a
	// failure leaves them for the next try.
	c.mu.Lock()
	busy, stopping := c.occupied(), c.stopping
	c.mu.Unlock()
	if stopping {
		return false
	}
	session, err := c.store.NextTurn(context.Background(), busy...)
	if err != nil || session == "" {
		if err != nil {
			log.Printf("chat: inbox: %v", err)
		}
		return false
	}
	choice, err := c.choiceOf(session)
	if err != nil {
		log.Printf("chat: session %s: %v", session, err)
		return false
	}
	// den may have started compacting the session meanwhile.
	release, ok := c.claim(session)
	if !ok {
		return false
	}
	messages, err := c.store.TakeTurn(context.Background(), session)
	if err != nil || len(messages) == 0 {
		release()
		if err != nil {
			log.Printf("chat: inbox: %v", err)
		}
		return false
	}
	joined := session == c.cursor(store.Telegram) || session == c.cursor(appClient)

	t := chatTurn{session: session, choice: choice, id: rand.Text(), untitled: c.untitled(session), telegram: c.inTelegram(session)}
	var texts []string
	for _, m := range messages {
		t.asked = t.asked || !m.Report
		texts = append(texts, m.Text)
		if m.Image != "" {
			t.images = append(t.images, m.Image)
		}
	}
	t.prompt = strings.Join(texts, "\n\n")
	run, stop := context.WithCancel(ctx)
	c.mu.Lock()
	t.own = !joined || c.stop != nil
	c.attached[session] = []Upload{}
	if t.own {
		c.bg[t.id] = stop
		t.agent = runner.NewAgent(c.cfg)
		t.agent.OnDropped = c.undelivered
		t.agent.OnTurn = c.steeredTurn
	} else {
		c.stop = stop
		t.agent = c.agent
	}
	// The agent starts a session den named before its first turn.
	c.steering[session] = t.agent
	c.mu.Unlock()
	if t.own {
		// The chat's agent may hold the session as it was before this turn.
		c.agent.Drop(session)
	}

	c.background.Go(func() {
		defer release()
		defer stop()
		c.run(ctx, run, t)
	})
	return true
}

// step records text as the latest step of session's running turn, and tells
// the app; an empty text only records that the turn runs.
func (c *Chat) step(session, text string) {
	c.mu.Lock()
	c.steps[session] = text
	c.mu.Unlock()
	if text != "" {
		c.Events.Publish(events.Event{Type: events.Step, Session: session, Text: text})
	}
}

// chatTurn is a turn of the chat's, from the messages it runs.
type chatTurn struct {
	id      string
	session string
	choice  runner.Choice
	prompt  string
	images  []string
	// asked says the user wrote, and telegram that Telegram's chat is in
	// the session, so the turn answers there as well as in the app.
	asked, telegram bool
	untitled        bool
	// own marks a turn on an agent of its own rather than the chat's.
	own   bool
	agent *runner.Agent
}

// run runs t until turn ends, and sends its reply. ctx is the chat's,
// which outlives turn to send what it said.
func (c *Chat) run(ctx, turn context.Context, t chatTurn) {
	session := t.session
	started := time.Now()
	c.step(session, "")
	defer func() {
		c.mu.Lock()
		delete(c.steps, session)
		c.mu.Unlock()
	}()
	// A chat session gets its title from the user's first message, while
	// its first turn runs.
	if t.untitled && t.asked {
		go c.title(session, t.prompt)
	}

	// A turn on an agent of its own shows no steps, as /bg does: the
	// chat's progress message is its agent's.
	done := make(chan struct{})
	shown := make(chan struct{})
	steps := make(chan progress, 64)
	if t.telegram {
		go c.showTyping(turn, done)
	}
	go func() {
		c.showProgress(ctx, steps, done, t.telegram && !t.own)
		close(shown)
	}()
	req := runner.Request{
		Prompt:   t.prompt,
		Provider: t.choice.Provider,
		Model:    t.choice.Model,
		Effort:   t.choice.Effort,
		Resume:   session,
		Images:   t.images,
		Progress: func(line string) {
			if text, ok := step(line); ok {
				c.step(session, text)
			}
			if t.own {
				return
			}
			select {
			case steps <- progress{line: line}:
			default:
			}
		},
		// What the agent said before a step goes under the step in the
		// chat's progress message; the app's one line keeps the step.
		Said: func(text string) {
			if t.own {
				return
			}
			select {
			case steps <- progress{said: text}:
			default:
			}
		},
	}
	reply := t.agent.Ask(turn, req)
	close(done)
	<-shown
	quiet := c.Quieted(session, started)

	c.mu.Lock()
	files := c.attached[session]
	delete(c.attached, session)
	delete(c.steering, session)
	if t.own {
		delete(c.bg, t.id)
	} else {
		c.stop = nil
	}
	c.mu.Unlock()
	if t.own {
		// A message fx runs as a turn of its own after this one ends
		// before fx stops, and the session stays busy until then.
		defer func() {
			t.agent.Settle(turn)
			t.agent.Close()
		}()
	} else {
		// The user's time away counts from the end of the chat's turn.
		select {
		case c.ended <- struct{}{}:
		default:
		}
	}

	if err := c.store.Touch(context.Background(), session); err != nil {
		log.Printf("chat: touch %s: %v", session, err)
	}
	// A turn tells how big its session has grown, and the first reply
	// after den compacted a session says so.
	compacted := c.measure(session, started, reply.Error == "")

	log.Printf("chat: turn in %s", reply.Duration)
	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	// The app hears the turn end however it ends.
	defer c.Events.Publish(events.Event{Type: events.Idle, Session: session})
	if turn.Err() == context.Canceled {
		// /stop already answered; den stopping says so.
		if ctx.Err() != nil {
			c.cutShort(session)
		}
		return
	}
	// A turn may end without a word, such as one that answers a plain "ok"
	// or that the agent marked quiet; files it sent go without a word.
	silent := false
	if reply.Error == "" && quiet {
		if len(files) == 0 {
			log.Printf("chat: the turn had nothing to tell")
			return
		}
		silent = true
	}

	text := ""
	if !silent {
		text = answer(reply, !reply.New)
	}
	// The app shows each session apart, so its reply needs no label.
	c.Events.Publish(events.Event{Type: events.Reply, Session: session, Text: text, Failed: reply.Error != "", Files: c.keep(session, files)})
	// A reply from a session other than the chat's, such as a reply to a
	// job's message or a turn the chat left with /new, says which.
	if label := c.Label(session); label != "" {
		text = label + "\n\n" + text
	}
	text = compacted + text
	if !t.telegram {
		return
	}

	if telegram, err := notify.Load(c.cfg); err == nil {
		call, cancel := context.WithTimeout(ctx, time.Minute)
		ids, err := telegram.SendIDs(call, text)
		cancel()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
		if len(files) > 0 {
			call, cancel := context.WithTimeout(ctx, 5*time.Minute)
			sent, err := telegram.SendFiles(call, uploads(files))
			cancel()
			if err != nil {
				log.Printf("chat: send files: %v", err)
			}
			ids = append(ids, sent...)
		}
		c.Sent(ids, session)
	}
}

// measure records session's size after a turn that started then, and
// returns the line that tells the user den compacted it while they were
// away, once, after a turn that did not fail.
func (c *Chat) measure(session string, started time.Time, ok bool) string {
	ctx := context.Background()
	size := sessions.ContextSize(c.cfg.Sessions, session, started)
	if size > 0 {
		if err := c.store.SetContext(ctx, session, size); err != nil {
			log.Printf("chat: context of %s: %v", session, err)
		}
	}
	s, err := c.store.Session(ctx, session)
	if err != nil || s.Compacted == 0 || !ok {
		return ""
	}
	if err := c.store.SetCompacted(ctx, session, 0); err != nil {
		log.Printf("chat: compacted %s: %v", session, err)
	}
	line := "🗜 While you were away, den compacted this session"
	if s.Context > 0 {
		line += fmt.Sprintf(": %s → %s tokens", tokens(s.Compacted), tokens(s.Context))
	}
	return line + ".\n\n"
}

// fxErrors say the errors fx names that the user can act on.
var fxErrors = map[string]string{
	"SubscriptionNativeImageUnavailable": "This model cannot see images. Pick another with /model and send the image again.",
}

// answer is what the user sees of reply: its text, or why fx failed.
func answer(reply runner.Reply, resumed bool) string {
	if text, ok := fxErrors[reply.Error]; ok {
		return text
	}
	switch {
	case reply.Error != "":
		text := "fx failed: " + reply.Error
		if line := lastLine(reply.Stderr); line != "" {
			text += "\n" + line
		}
		if resumed {
			text += "\nSend /new if this session cannot continue."
		}
		return text
	case reply.Output == "":
		return "(no reply)"
	}
	return reply.Output
}
