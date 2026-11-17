package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/sessions"
)

// What the app asks of the chat. The app and Telegram share the chat: its
// session, its models, and its turns. A turn of the app's messages answers
// only in the app, and one of Telegram's answers in both.

// Post gives text from the app to the chat's session: it joins the running
// turn, or waits for the next one.
func (c *Chat) Post(ctx context.Context, text string) {
	e := entry{text: text, app: true}
	c.published([]entry{e})
	if c.steer(ctx, notify.Message{}, []entry{e}) {
		return
	}
	c.mu.Lock()
	c.inbox = append(c.inbox, e)
	c.save()
	c.mu.Unlock()
	c.poke()
}

// published tells the app of the user's messages among entries.
func (c *Chat) published(entries []entry) {
	session := c.session()
	for _, e := range entries {
		if !e.report && e.text != "" {
			to := session
			if e.session != "" {
				to = e.session
			}
			c.Events.Publish(events.Event{Type: events.Message, Session: to, Text: e.text})
		}
	}
}

// Stop stops what runs, as /stop does, and reports whether anything did.
func (c *Chat) Stop(ctx context.Context) bool {
	stopped, notices := c.halt()
	c.tell(ctx, nil, notices)
	return stopped
}

// Open makes the chat go on in session, or start a new one when it is
// empty.
func (c *Chat) Open(ctx context.Context, session string) error {
	if session != "" {
		list, err := sessions.List(c.cfg.Sessions, 1<<20)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(list, func(s sessions.Summary) bool { return s.ID == session }) {
			return fmt.Errorf("no session %s", session)
		}
	}
	c.tell(ctx, nil, c.switchTo(session))
	return nil
}

// Model is a model with what the app shows of it.
type Model struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Name is the catalog's name for it, such as "GPT-5.6 Terra", or the
	// model's ID when the catalog has none.
	Name   string `json:"name"`
	Effort string `json:"effort,omitempty"`
	// Efforts are its levels, none when it has no choice.
	Efforts []string `json:"efforts,omitempty"`
}

// Setup is what the app shows of the chat.
type Setup struct {
	Session string `json:"session,omitempty"`
	Busy    bool   `json:"busy"`
	Model   Model  `json:"model"`
	// Second is the model the jobs set to it run on, nil without one.
	Second *Model `json:"second,omitempty"`
	// Recent are the models chosen last, newest first.
	Recent    []Model  `json:"recent"`
	Providers []string `json:"providers"`
}

// Setup reads the chat's session, its models, and whether a turn runs.
func (c *Chat) Setup(ctx context.Context) (Setup, error) {
	setup, provider, model, err := c.current(ctx)
	if err != nil {
		return Setup{}, err
	}

	c.mu.Lock()
	s := Setup{Session: c.state.Session, Busy: c.stop != nil, Providers: setup.Providers}
	effort, second, recent := c.state.Effort, c.state.Second, slices.Clone(c.state.Recent)
	c.mu.Unlock()

	s.Model = c.describe(provider, model, effort)
	if second != nil {
		m := c.describe(second.Provider, second.Model, second.Effort)
		s.Second = &m
	}
	for _, ref := range recent {
		p, m, _ := strings.Cut(ref, "/")
		s.Recent = append(s.Recent, c.describe(p, m, ""))
	}
	return s, nil
}

// Models lists provider's models, the provider fx uses when it is empty.
func (c *Chat) Models(ctx context.Context, provider string) ([]Model, error) {
	if provider == "" {
		setup, err := runner.Status(ctx, c.cfg, "")
		if err != nil {
			return nil, err
		}
		provider = setup.Provider
	}
	ids, err := runner.Models(ctx, c.cfg, provider)
	if err != nil {
		return nil, err
	}
	list := make([]Model, 0, len(ids))
	for _, id := range ids {
		list = append(list, c.describe(provider, id, ""))
	}
	return list, nil
}

// ChooseModel, ChooseEffort, ChooseSecond, and ChooseSecondEffort change
// what /model and /effort change.
func (c *Chat) ChooseModel(ctx context.Context, provider, model string) error {
	_, err := c.chooseModel(ctx, provider, model)
	return c.changed(err)
}

func (c *Chat) ChooseEffort(ctx context.Context, level string) error {
	_, err := c.chooseEffort(ctx, cmpDefault(level))
	return c.changed(err)
}

// ChooseSecond removes the second model when model is empty.
func (c *Chat) ChooseSecond(ctx context.Context, provider, model string) error {
	_, _, err := c.chooseSecond(ctx, provider, model)
	return c.changed(err)
}

func (c *Chat) ChooseSecondEffort(ctx context.Context, level string) error {
	_, err := c.chooseSecondEffort(ctx, cmpDefault(level))
	return c.changed(err)
}

// changed tells the app the settings changed, unless err says they did not.
func (c *Chat) changed(err error) error {
	if err == nil {
		c.Events.Publish(events.Event{Type: events.Settings, Session: c.session()})
	}
	return err
}

// cmpDefault names the model's own level as /effort does.
func cmpDefault(level string) string {
	if level == "" {
		return "default"
	}
	return level
}

// describe names a model as the app shows it.
func (c *Chat) describe(provider, model, effort string) Model {
	name := c.catalogName(provider, model)
	if name == "" {
		name = model
	}
	return Model{Provider: provider, Model: model, Name: name, Effort: effort, Efforts: c.efforts(provider, model)}
}

// catalogName reads the model's name from fx's catalog of the provider, or
// "" when it has none.
func (c *Chat) catalogName(provider, model string) string {
	home := filepath.Dir(filepath.Dir(c.cfg.Sessions))
	data, err := os.ReadFile(filepath.Join(home, "model-catalogs", provider+".json"))
	if err != nil {
		return ""
	}
	var catalog map[string]struct {
		Name string `json:"name"`
	}
	json.Unmarshal(data, &catalog)
	return catalog[model].Name
}
