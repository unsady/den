package chat

import (
	"context"
	"crypto/rand"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
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

// bgName is a short name for a task, from its first words, such as
// compare-two-models.
func bgName(task string) string {
	words := strings.FieldsFunc(strings.ToLower(task), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > 3 {
		words = words[:3]
	}
	if len(words) == 0 {
		return "task"
	}
	return clipRunes(strings.Join(words, "-"), 30)
}

// runBackground runs task in a new session of its own besides the chat's
// turns. Its answer comes headed with the task's name, and a reply to it
// goes on in its session.
func (c *Chat) runBackground(ctx context.Context, telegram *notify.Telegram, task string) {
	if task == "" {
		c.send(ctx, telegram, "Send /bg with a task, such as: /bg compare these two models")
		return
	}
	label := "⚙ " + bgName(task)
	id := rand.Text()
	run, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	c.bg[id] = cancel
	c.turns = append(c.turns, turnRef{id: id, generation: c.generation, label: label, bg: true})
	if len(c.turns) > keptTurns {
		c.turns = c.turns[len(c.turns)-keptTurns:]
	}
	c.mu.Unlock()
	c.send(ctx, telegram, label+" runs in the background. Its answer comes here; reply to it to go on there.")

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

		// Reports that came while the turn ran go to its session now that
		// it is known, or to the user when there is none.
		c.mu.Lock()
		delete(c.bg, id)
		var pending []entry
		for i := range c.turns {
			if c.turns[i].id == id {
				c.turns[i].session = reply.SessionID
				pending, c.turns[i].pending = c.turns[i].pending, nil
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
		c.mu.Unlock()
		if len(pending) > len(notices) {
			c.poke()
		}

		if run.Err() == context.Canceled {
			// /stop already answered, or den is shutting down.
			return
		}
		telegram, err := notify.Load(c.cfg)
		if err != nil {
			return
		}
		c.tell(ctx, telegram, notices)
		call, done := context.WithTimeout(ctx, time.Minute)
		ids, err := telegram.SendIDs(call, label+"\n\n"+answer(reply, false))
		done()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
		c.Sent(ids, reply.SessionID, label)
	})
}
