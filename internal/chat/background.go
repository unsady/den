package chat

import (
	"context"
	"crypto/rand"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/mapper"
	"github.com/unsady/den/internal/models"
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

// nameURL is where den asks provider's models, through its proxy: path is
// chat/completions, or responses for a subscription that takes only those.
// Tests point it elsewhere.
var nameURL = func(provider, path string) string {
	return "http://" + models.Proxy + "/" + provider + "/" + path
}

// nameWait bounds the request for a name, so a slow model does not hold
// the task's name back long.
const nameWait = 30 * time.Second

// nameTask asks a model for a short name for task, such as
// compare-models: the second model when the chat has one, since it is
// usually the cheaper, or the main one. It returns "" when the request
// fails.
func (c *Chat) nameTask(ctx context.Context, task string) string {
	c.mu.Lock()
	provider, model := c.state.Provider, c.state.Model
	if s := c.state.Second; s != nil {
		provider, model = s.Provider, s.Model
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, nameWait)
	defer cancel()
	if provider == "" || model == "" {
		setup, err := runner.Status(ctx, c.cfg, provider)
		if err != nil {
			return ""
		}
		provider = setup.Provider
		if model == "" {
			model = setup.Model
		}
	}
	if provider == "" || model == "" {
		return ""
	}

	// The ChatGPT and Grok subscriptions take only the Responses API.
	responses := provider == "codex" || provider == "grok"
	path := "chat/completions"
	if responses {
		path = "responses"
	}
	reply, err := mapper.Ask(ctx, mapper.Options{
		URL:          nameURL(provider, path),
		Responses:    responses,
		Model:        model,
		Instructions: "Name the user's task in English, in two words or three at most, lowercase and joined with hyphens, such as compare-models. Answer with the name only.",
	}, clipRunes(task, 2000))
	if err != nil {
		log.Printf("chat: naming a task: %v", err)
		return ""
	}
	return cleanName(reply)
}

// cleanName keeps a model's name for a task to lowercase words joined with
// hyphens, at most 30 characters.
func cleanName(text string) string {
	text = strings.TrimSpace(text)
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = line
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	name := strings.Join(words, "-")
	if r := []rune(name); len(r) > 30 {
		name = strings.TrimRight(string(r[:30]), "-")
	}
	return name
}

// runBackground runs task in a new session of its own besides the chat's
// turns. Its answer comes headed with the task's name, and a reply to it
// goes on in its session.
func (c *Chat) runBackground(ctx context.Context, telegram *notify.Telegram, task string) {
	if task == "" {
		c.send(ctx, telegram, "Send /bg with a task, such as: /bg compare these two models")
		return
	}
	id := rand.Text()
	asked := time.Now()
	run, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	c.bg[id] = cancel
	c.turns = append(c.turns, turnRef{id: id, generation: c.generation, bg: true})
	if len(c.turns) > keptTurns {
		c.turns = c.turns[len(c.turns)-keptTurns:]
	}
	c.mu.Unlock()
	const after = " Its answer comes here; reply to it to go on there."
	call, done := context.WithTimeout(ctx, time.Minute)
	started, err := telegram.Post(call, "⚙ Background task started."+after)
	done()
	if err != nil {
		log.Printf("chat: send: %v", err)
	}

	// A model names the task while it runs, and the message says the name
	// once it has it.
	named := make(chan string, 1)
	c.background.Go(func() {
		name := c.nameTask(run, task)
		if name == "" {
			name = bgName(task)
		}
		label := "⚙ " + name
		named <- label
		if started != 0 {
			c.editPlain(ctx, telegram, started, label+" runs in the background."+after)
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

		// Reports that came while the turn ran go to its session now that
		// it is known, or to the user when there is none.
		label := <-named
		c.mu.Lock()
		delete(c.bg, id)
		var pending []entry
		for i := range c.turns {
			if c.turns[i].id == id {
				c.turns[i].session = reply.SessionID
				c.turns[i].label = label
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
			e.session, e.label = reply.SessionID, label
			c.inbox = append(c.inbox, e)
		}
		c.save()
		c.mu.Unlock()
		if len(pending) > len(notices) {
			c.poke()
		}

		if run.Err() == context.Canceled {
			// /stop already answered, or den is shutting down.
			return
		}
		if reply.SessionID != "" {
			c.Events.Publish(events.Event{Type: events.Message, At: asked, Session: reply.SessionID, Title: label, Text: task})
			c.Events.Publish(events.Event{Type: events.Reply, Session: reply.SessionID, Title: label, Text: answer(reply, false), Failed: reply.Error != ""})
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
		c.Sent(ids, reply.SessionID, label, &runner.Choice{Provider: provider, Model: model, Effort: effort})
	})
}
