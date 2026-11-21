package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
)

// What the app asks of the chat. The app and Telegram share the chat: its
// sessions, its models, and its turns. Every reply comes in the app, and in
// Telegram when its chat is in the reply's session.

// File is a file the app sends with a message.
type File struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// images are the kinds of picture a model sees when attached to a turn.
var pictures = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

// Post gives text and files from the app to session, the chat's when it is
// empty. In the chat's session it joins the running turn, or waits for the
// next one; in another, such as a job run's or one the chat has left, it
// waits for a turn of its own, as a reply to a job's message does. A /bg
// task starts in the background. Files are saved in the inbox as Telegram's are, and pictures are
// attached to the turn.
func (c *Chat) Post(ctx context.Context, text, session string, files []File) error {
	// A /bg task runs in a session of its own, as it does from Telegram.
	if task, ok := bgTask(text); ok && len(files) == 0 {
		c.runBackground(ctx, nil, task)
		return nil
	}
	return c.post(ctx, text, session, files)
}

// Start gives text and files from the app to a new session of their own,
// which waits for a turn as any other, and returns the session's ID. A /bg
// task there is only text.
func (c *Chat) Start(ctx context.Context, text string, files []File) (string, error) {
	session := newSession()
	if err := c.store.AddSession(context.Background(), store.Session{ID: session, Kind: store.KindChat}); err != nil {
		return "", err
	}

	return session, c.post(ctx, text, session, files)
}

// post gives text and files to session, the chat's when it is empty.
func (c *Chat) post(ctx context.Context, text, session string, files []File) error {
	entries := []entry{{text: text}}
	if len(files) > 0 {
		entries = nil
		stamp := time.Now().UnixMilli()
		for i, f := range files {
			path, err := c.writeFile(ctx, fmt.Sprintf("%d-%d-%s", stamp, i, fileName(&notify.File{Name: f.Name})), f.Data)
			if err != nil {
				return err
			}

			file := &Upload{Upload: notify.Upload{Name: f.Name, Data: f.Data}}
			e := entry{text: "[The user sent a file, saved at " + path + "]", file: file}
			if slices.Contains(pictures, http.DetectContentType(f.Data)) {
				e = entry{text: "[The user sent a picture, attached and saved at " + path + "]", image: path, file: file}
			}
			entries = append(entries, e)
		}
		// The user's words come first and den's notes about the files after
		// them.
		if caption := strings.TrimSpace(text); caption != "" {
			entries[0].text = caption + "\n" + entries[0].text
			entries[0].shown = caption
		}
	}

	if session == "" {
		var err error
		if session, err = c.joined(appClient); err != nil {
			return err
		}
	}
	for i := range entries {
		entries[i].session = session
	}

	c.published(entries)
	if c.steer(ctx, entries) {
		return nil
	}
	return c.enqueue(entries...)
}

// published tells the app of the user's message in entries, as the user
// wrote it: one message, though den gives the agent each file apart, with
// the files kept for the app. A file den could not keep shows as its name.
func (c *Chat) published(entries []entry) {
	var lines []string
	var files []events.Attachment
	session := ""
	for _, e := range entries {
		if e.report || e.text == "" {
			continue
		}
		session = e.session

		text := e.text
		if e.shown != "" || e.file != nil {
			text = e.shown
		}
		if text != "" {
			lines = append(lines, text)
		}
		if e.file == nil {
			continue
		}
		if kept := c.keep(session, []Upload{*e.file}); len(kept) > 0 {
			files = append(files, kept...)
		} else {
			lines = append(lines, "📎 "+e.file.Name)
		}
	}
	if len(lines) == 0 && len(files) == 0 {
		return
	}

	c.Events.Publish(events.Event{Type: events.Message, Session: session, Text: strings.Join(lines, "\n"), Files: files})
}

// Stop stops what runs, as /stop does, and reports whether anything did.
func (c *Chat) Stop(ctx context.Context) bool {
	stopped, notices := c.halt()
	c.tell(ctx, nil, notices)
	return stopped
}

// Open makes the app go on in session, or start a new one with its next
// message when session is empty.
func (c *Chat) Open(ctx context.Context, session string) error {
	if session != "" {
		if _, err := c.store.Session(ctx, session); err != nil {
			return fmt.Errorf("no session %s", session)
		}
	}
	if err := c.enter(appClient, session); err != nil {
		return err
	}
	c.Events.Publish(events.Event{Type: events.Settings, Session: session})
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
	// Running holds the latest step of each session whose turn runs, empty
	// until its first.
	Running map[string]string `json:"running"`
	Model   Model             `json:"model"`
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
	s := Setup{Session: c.cursor(appClient), Running: maps.Clone(c.steps), Providers: setup.Providers}
	effort, second, recent := c.mainModel().Effort, c.second(), c.recent()
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
		c.Events.Publish(events.Event{Type: events.Settings, Session: c.cursor(appClient)})
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
