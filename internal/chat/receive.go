package chat

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/unsady/den/internal/acp"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
)

func (c *Chat) receive(ctx context.Context, telegram *notify.Telegram, m notify.Message) {
	if m.Callback != nil {
		c.press(ctx, telegram, *m.Callback)
		return
	}
	if m.Other {
		c.send(ctx, telegram, "den reads text, photos, voice messages, and files for now.")
		return
	}
	if m.File != nil {
		c.receiveFile(ctx, telegram, m)
		return
	}

	if task, ok := bgTask(m.Text); ok {
		c.runBackground(ctx, telegram, task)
		return
	}

	name, arg := command(m.Text)
	if arg != "" && name != "/model" && name != "/effort" && name != "/sessions" {
		name = ""
	}
	switch name {
	case "/start", "/help":
		c.send(ctx, telegram, help())

	case "/new":
		c.switchTo("")
		text := "✦ New session"
		if model := c.modelName(ctx); model != "" {
			text += " · " + model
		}
		c.sendHTML(ctx, telegram, text, nil)

	case "/model":
		if arg == "" {
			c.recentModels(ctx, telegram, 0)
		} else {
			c.findModel(ctx, telegram, arg)
		}

	case "/effort":
		if arg == "" {
			c.listEfforts(ctx, telegram)
		} else {
			text, err := c.chooseEffort(ctx, arg)
			c.reply(ctx, telegram, text, err)
		}

	case "/sessions":
		c.listSessions(ctx, telegram, arg, false, 0)

	case "/jobs":
		c.listJobs(ctx, telegram, 0)

	case "/log":
		c.showLog(ctx, telegram, 0)

	case "/status":
		c.sendHTML(ctx, telegram, c.status(ctx), nil)

	case "/bug":
		c.send(ctx, telegram, bugReport)

	case "/usage":
		c.send(ctx, telegram, c.usage.Report(time.Now()))

	case "/compact":
		c.mu.Lock()
		busy := c.stop != nil
		c.mu.Unlock()
		select {
		case c.squeeze <- struct{}{}:
			if busy {
				c.send(ctx, telegram, "den compacts the session after this turn.")
			}
		default:
		}

	case "/stop":
		stopped, notices := c.halt()
		if stopped {
			c.send(ctx, telegram, "Stopped.")
		} else {
			c.send(ctx, telegram, "Nothing is running.")
		}
		c.tell(ctx, telegram, notices)

	default:
		c.queue(ctx, telegram, m, entry{text: quoted(m, m.Text), shown: m.Text})
	}
}

// halt stops the running turns, /bg's too, and drops what waits for the
// next turn. It reports whether anything ran, and returns what den's
// dropped reports would have told the agent, for the user.
func (c *Chat) halt() (bool, []string) {
	var notices []string
	dropped, err := c.store.ClearInbox(context.Background())
	if err != nil {
		log.Printf("chat: inbox: %v", err)
	}
	for _, m := range dropped {
		if m.Report && m.Notice != "" {
			notices = append(notices, m.Notice)
		}
	}

	c.mu.Lock()
	stop := c.stop
	clear(c.steered)
	var stops []context.CancelFunc
	for _, s := range c.bg {
		stops = append(stops, s)
	}
	c.mu.Unlock()
	if stop != nil {
		stops = append(stops, stop)
	}
	for _, s := range stops {
		s()
	}
	return len(stops) > 0, notices
}

// quoted adds the part of an earlier message that m replies to after text.
// It carries only a few hundred characters: enough to say which message is
// meant, since the bot's own replies are already in the session.
func quoted(m notify.Message, text string) string {
	if m.Quote == "" {
		return text
	}
	who := "the user's earlier message"
	if m.QuotesBot {
		who = "your earlier message"
	}
	return text + "\n[In reply to " + who + ": «" + m.Quote + "»]"
}

// switchTo puts Telegram's chat in session, or makes its next message start
// a new one when session is empty. Turns that run in the session it leaves
// go on there.
func (c *Chat) switchTo(session string) {
	if err := c.enter(store.Telegram, session); err != nil {
		log.Printf("chat: switch: %v", err)
	}
	c.Events.Publish(events.Event{Type: events.Settings, Session: session})
}

// tell sends the user den's reports their agent did not get.
// Without telegram, as for the app, only the app hears them.
func (c *Chat) tell(ctx context.Context, telegram *notify.Telegram, notices []string) {
	for _, n := range notices {
		c.Events.Publish(events.Event{Type: events.Notice, Text: n})
		if telegram != nil {
			c.send(ctx, telegram, n)
		}
	}
}

// queue keeps a message for the next turn in its session. While a turn
// runs there, it marks the message with 👀, so the user sees it waits.
//
// A text for the chat's own session joins the running turn instead, when it
// is one of the agent's, and is marked with ✍.
func (c *Chat) queue(ctx context.Context, telegram *notify.Telegram, m notify.Message, entries ...entry) {
	id := m.ID
	session, err := c.target(m.ReplyTo)
	if err != nil {
		log.Printf("chat: queue: %v", err)
		c.send(ctx, telegram, "den could not keep the message: "+err.Error())
		return
	}
	for i := range entries {
		entries[i].session = session
	}

	c.published(entries)
	if c.steer(ctx, entries) {
		c.react(ctx, telegram, id, "✍")
		return
	}

	if err := c.enqueue(entries...); err != nil {
		log.Printf("chat: queue: %v", err)
		c.send(ctx, telegram, "den could not keep the message: "+err.Error())
		return
	}
	c.mu.Lock()
	busy := c.busy[session] > 0
	c.mu.Unlock()

	if busy {
		c.react(ctx, telegram, id, "👀")
	}
}

// steer passes the texts of entries to the running turn, when it is the
// agent's and they are for its session. It reports false when the caller
// keeps them for the next turn instead.
func (c *Chat) steer(ctx context.Context, entries []entry) bool {
	c.mu.Lock()
	session := c.steering
	ok := session != ""
	var texts []string
	for _, e := range entries {
		ok = ok && e.session == session && e.image == "" && !e.report
		texts = append(texts, e.text)
	}
	c.mu.Unlock()
	if !ok || len(texts) == 0 {
		return false
	}

	text := strings.Join(texts, "\n\n")
	c.mu.Lock()
	c.steered[text] = steered{session: session, at: time.Now()}
	c.mu.Unlock()
	if err := c.agent.Steer(ctx, text); err != nil {
		c.mu.Lock()
		delete(c.steered, text)
		c.mu.Unlock()
		if !errors.Is(err, acp.ErrIdle) {
			log.Printf("chat: steer: %v", err)
		}
		return false
	}
	return true
}

// undelivered runs a message passed to a turn that fx did not deliver as a
// turn of its own in its session, unless /stop dropped it meanwhile.
func (c *Chat) undelivered(text string) {
	c.mu.Lock()
	st, kept := c.steered[text]
	delete(c.steered, text)
	c.mu.Unlock()
	if !kept {
		return
	}
	if err := c.enqueue(entry{text: text, session: st.session}); err != nil {
		log.Printf("chat: undelivered message: %v", err)
	}
}

// steeredTurn sends the reply to a message passed to a turn that fx ran as a
// turn of its own, since the turn it was meant for had just ended.
func (c *Chat) steeredTurn(text string, reply runner.Reply) {
	c.mu.Lock()
	st := c.steered[text]
	delete(c.steered, text)
	c.mu.Unlock()
	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	// fx ran the message as a turn after den passed it, so a mark from
	// before that belongs to the turn it was meant for.
	quiet := c.Quieted(reply.SessionID, st.at)
	if reply.Error == "" && (quiet || runner.LegacySilent(reply.Output, reply.SessionID)) {
		return
	}
	c.Events.Publish(events.Event{Type: events.Reply, Session: reply.SessionID, Text: answer(reply, true), Failed: reply.Error != ""})

	telegram, err := notify.Load(c.cfg)
	if err != nil {
		return
	}
	call, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out := answer(reply, true)
	if label := c.Label(reply.SessionID); label != "" {
		out = label + "\n\n" + out
	}
	ids, err := telegram.SendIDs(call, out)
	if err != nil {
		log.Printf("chat: send: %v", err)
	}
	c.Sent(ids, reply.SessionID)
}

// react marks the user's message id, unless it is 0.
func (c *Chat) react(ctx context.Context, telegram *notify.Telegram, id int64, emoji string) {
	if id == 0 {
		return
	}
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.React(call, id, emoji); err != nil {
		log.Printf("chat: react: %v", err)
	}
}

// command returns the bot command that text starts with, without a bot name
// suffix, and the text after it on the same line. name is "" when text is
// not a command.
func command(text string) (name, arg string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return "", ""
	}
	name, arg, _ = strings.Cut(text, " ")
	name, _, _ = strings.Cut(name, "@")
	return strings.ToLower(name), strings.TrimSpace(arg)
}
