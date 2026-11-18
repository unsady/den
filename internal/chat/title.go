package chat

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/unsady/den/internal/claude"
	"github.com/unsady/den/internal/models"
	"github.com/unsady/den/internal/runner"
)

// titleTimeout bounds the making of one title.
const titleTimeout = 2 * time.Minute

// titlePrompt asks for a session's title and short name from its first
// exchange.
const titlePrompt = "Write a title of 2 to 6 words for a conversation that starts as below, and a name of 1 or 2 words for its tab, both in the user's language, which is the assistant's when the user sent only a link, a file, or a photo. Reply with two lines, the title on the first and the name on the second, without quotes or periods.\n\n"

// name makes a title and a short name for a new session from its first
// exchange, the user's message and the reply, and keeps the title in place
// of fx's. The reply says what
// a photo, a link, or a short message was about. fx titles the sessions of
// Codex and Grok itself, and those of other providers with the message
// alone. Claude answers through the claude command, the other providers
// through den's model proxy. A failure leaves fx's title.
func (c *Chat) name(session, message, reply, provider, model string) {
	if strings.TrimSpace(message) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()

	// The chat may leave the provider and the model to fx's defaults.
	if provider == "" || model == "" {
		setup, err := runner.Status(ctx, c.cfg, provider)
		if err != nil {
			log.Printf("chat: title of %s: %v", session, err)
			return
		}
		provider = cmp.Or(provider, setup.Provider)
		model = cmp.Or(model, setup.Model)
	}
	if provider == "codex" || provider == "grok" {
		return
	}

	prompt := titlePrompt + "User:\n" + clipRunes(message, 2000)
	if strings.TrimSpace(reply) != "" {
		prompt += "\n\nAssistant:\n" + clipRunes(reply, 1500)
	}
	var answer string
	var err error
	if provider == claude.Provider {
		answer, err = claude.New(c.cfg).Ask(ctx, "haiku", prompt)
	} else {
		answer, err = complete(ctx, provider, model, prompt)
	}
	if err != nil {
		log.Printf("chat: title of %s: %v", session, err)
		return
	}

	title, name := titleLines(answer)
	if title == "" {
		return
	}
	c.Events.Name(session, clipRunes(title, 80), clipRunes(name, 24))
}

// titleLines reads the title and the name from the last two lines of
// answer, which a model may open with words of its own. A lone line is the
// title.
func titleLines(answer string) (title, name string) {
	var lines []string
	for line := range strings.Lines(answer) {
		if line = strings.Trim(strings.TrimSpace(line), "\"'«»“”."); line != "" {
			lines = append(lines, line)
		}
	}
	switch len(lines) {
	case 0:
		return "", ""
	case 1:
		return lines[0], ""
	}
	return lines[len(lines)-2], lines[len(lines)-1]
}

// complete asks provider's model for one answer to prompt through den's
// model proxy, which speaks OpenAI's chat completions.
func complete(ctx context.Context, provider, model, prompt string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+models.Proxy+"/"+provider+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered HTTP %d", provider, resp.StatusCode)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s answered with no choice", provider)
	}
	return out.Choices[0].Message.Content, nil
}
