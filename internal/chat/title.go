package chat

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/mapper"
	"github.com/unsady/den/internal/models"
	"github.com/unsady/den/internal/runner"
)

// One-shot questions to a model: a session's title and a /bg task's name.
// They go through den's model proxy to the second model when the chat has
// one, since it is usually the cheaper, or to the main one.

// askURL is where den asks provider's models, through its proxy: path is
// chat/completions, or responses for a subscription that takes only those.
// Tests point it elsewhere.
var askURL = func(provider, path string) string {
	return "http://" + models.Proxy + "/" + provider + "/" + path
}

// askWait bounds one question, so a slow model does not hold a title or a
// name back long.
const askWait = time.Minute

// ask asks the chat's quick model instructions about input and returns its
// answer.
func (c *Chat) ask(ctx context.Context, instructions, input string) (string, error) {
	m := c.mainModel()
	if s := c.second(); s != nil {
		m = *s
	}
	provider, model := m.Provider, m.Model

	ctx, cancel := context.WithTimeout(ctx, askWait)
	defer cancel()
	if provider == "" || model == "" {
		setup, err := runner.Status(ctx, c.cfg, provider)
		if err != nil {
			return "", err
		}
		if provider == "" {
			provider = setup.Provider
		}
		if model == "" {
			model = setup.Model
		}
	}
	if provider == "" || model == "" {
		return "", errors.New("no model is set up")
	}

	// The ChatGPT and Grok subscriptions take only the Responses API.
	responses := provider == "codex" || provider == "grok"
	path := "chat/completions"
	if responses {
		path = "responses"
	}
	return mapper.Ask(ctx, mapper.Options{
		URL:          askURL(provider, path),
		Responses:    responses,
		Model:        model,
		Instructions: instructions,
	}, input)
}

// titleInstructions ask for a session's title from the user's first
// message.
const titleInstructions = "Write a title of 2 to 6 words for a conversation that starts with the user's message below, in the language of that message. Reply with the title alone, on one line, without quotes or a final period."

// maxTitle is the most runes a title keeps.
const maxTitle = 80

// makeTitle asks for a title for a conversation that starts with message,
// and returns "" when the model gives none.
func (c *Chat) makeTitle(ctx context.Context, message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	answer, err := c.ask(ctx, titleInstructions, clipRunes(message, 2000))
	if err != nil {
		log.Printf("chat: title: %v", err)
		return ""
	}
	title, ok := cleanTitle(answer)
	if !ok {
		log.Printf("chat: title: the model answered %q", clipRunes(answer, 100))
	}
	return title
}

// title names session from the user's first message in it. A failure
// leaves it untitled, and its next turn tries again.
func (c *Chat) title(session, message string) {
	title := c.makeTitle(context.Background(), message)
	if title == "" {
		return
	}

	set, err := c.store.SetTitle(context.Background(), session, title)
	if err != nil {
		log.Printf("chat: title of %s: %v", session, err)
		return
	}
	if set {
		c.Events.Publish(events.Event{Type: events.Titled, Session: session, Title: title})
	}
}

// cleanTitle takes a title from a model's answer: one line of at most
// maxTitle runes, without the quotes and the period a model may add. It
// reports false when the answer is no title.
func cleanTitle(answer string) (string, bool) {
	var lines []string
	for line := range strings.Lines(answer) {
		if line = strings.Trim(strings.TrimSpace(line), "\"'«»“”`*."); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return "", false
	}
	title := strings.Join(strings.Fields(lines[0]), " ")
	if n := len(strings.Fields(title)); n == 0 || n > 10 || len([]rune(title)) > maxTitle {
		return "", false
	}
	return title, true
}
