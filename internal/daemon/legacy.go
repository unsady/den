package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/sessions"
	"github.com/unsady/den/internal/store"
)

// importedKey marks a store that took what an older den kept in files.
const importedKey = "legacy.imported"

// legacyChoice is a model as chat.json kept it.
type legacyChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

// importLegacy fills the store, once, from the files an older den kept:
// fx's list of sessions, the titles den made in the journal, the job runs'
// sessions, and chat.json's session, models, offset, waiting messages, and
// the bot's messages. The files stay as they were. Every step but the
// waiting messages can be taken again, so an import that failed partway
// runs anew on the next start.
func importLegacy(ctx context.Context, cfg config.Config, db *store.Store) error {
	if done, err := db.Setting(ctx, importedKey); err != nil || done != "" {
		return err
	}
	list, err := sessions.List(cfg.Sessions, 1<<20)
	if err != nil {
		return err
	}

	var chat struct {
		Session  string          `json:"session"`
		Offset   int64           `json:"offset"`
		Provider string          `json:"provider"`
		Model    string          `json:"model"`
		Effort   string          `json:"effort"`
		Second   *legacyChoice   `json:"second"`
		Recent   []string        `json:"recent"`
		Inbox    []legacyMessage `json:"inbox"`
		Replies  []struct {
			Message int64         `json:"message"`
			Session string        `json:"session"`
			Choice  *legacyChoice `json:"choice"`
		} `json:"replies"`
	}
	if data, err := os.ReadFile(filepath.Join(cfg.State, "chat.json")); err == nil {
		if err := json.Unmarshal(data, &chat); err != nil {
			log.Printf("import: chat.json: %v", err)
		}
	}
	choices := map[string]store.Choice{}
	for _, r := range chat.Replies {
		if r.Choice != nil {
			choices[r.Session] = store.Choice{Provider: r.Choice.Provider, Model: r.Choice.Model, Effort: r.Choice.Effort}
		}
	}

	journal := filepath.Join(cfg.State, "journal")
	ofJobs := jobSessions(filepath.Join(cfg.State, "runs"))
	imported := map[string]bool{}
	for _, s := range list {
		session := store.Session{ID: s.ID, Kind: store.Chat, Created: s.Updated, Updated: s.Updated, Choice: choices[s.ID]}
		switch {
		case ofJobs[s.ID]:
			session.Kind = store.Job
		case startsNamed(filepath.Join(journal, s.ID+".jsonl")):
			session.Kind = store.Background
		}
		// fx's own title is the start of the first message; only den's
		// counts as one.
		if title, err := os.ReadFile(filepath.Join(journal, s.ID+".title")); err == nil {
			session.Title = strings.TrimSpace(string(title))
		}
		imported[s.ID] = true
		if _, err := db.Session(ctx, s.ID); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := db.AddSession(ctx, session); err != nil {
			return err
		}
	}

	for _, r := range chat.Replies {
		if imported[r.Session] {
			if err := db.SentTelegram(ctx, r.Session, r.Message); err != nil {
				return err
			}
		}
	}
	if imported[chat.Session] {
		if err := db.SetCursor(ctx, store.Telegram, chat.Session); err != nil {
			return err
		}
	}

	if err := importPrefs(ctx, db, chat.Provider, chat.Model, chat.Effort, chat.Second, chat.Recent, chat.Offset); err != nil {
		return err
	}
	// Waiting messages go to their sessions, the chat's own when an older
	// den kept none; those for a session fx never saved are dropped.
	var waiting []store.Message
	for _, m := range chat.Inbox {
		if m.Session == "" {
			m.Session = chat.Session
		}
		if imported[m.Session] {
			waiting = append(waiting, store.Message{Session: m.Session, Text: m.Text, Image: m.Image, App: m.App, Report: m.Report, Notice: m.Notice})
		}
	}
	if err := db.Enqueue(ctx, waiting...); err != nil {
		return err
	}

	log.Printf("import: %d sessions, %d waiting messages", len(imported), len(waiting))
	return db.SetSetting(ctx, importedKey, "1")
}

// legacyMessage is a waiting message as chat.json kept it.
type legacyMessage struct {
	Text    string `json:"text"`
	Image   string `json:"image"`
	App     bool   `json:"app"`
	Report  bool   `json:"report"`
	Notice  string `json:"notice"`
	Session string `json:"session"`
}

// importPrefs keeps the models the chat chose, oldest of the recent first
// so the newest ends up on top, and where it was in Telegram's updates.
func importPrefs(ctx context.Context, db *store.Store, provider, model, effort string, second *legacyChoice, recent []string, offset int64) error {
	if provider != "" || model != "" || effort != "" {
		if err := db.SetModel(ctx, store.Main, store.Choice{Provider: provider, Model: model, Effort: effort}); err != nil {
			return err
		}
	}
	if second != nil {
		if err := db.SetModel(ctx, store.Second, store.Choice(*second)); err != nil {
			return err
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if p, m, ok := strings.Cut(recent[i], "/"); ok {
			if err := db.UseModel(ctx, p, m); err != nil {
				return err
			}
		}
	}
	if offset > 0 {
		return db.SetTelegramOffset(ctx, offset)
	}
	return nil
}

// jobSessions reads the sessions of the job runs an older den listed in
// dir/sessions and in the runs' records.
func jobSessions(dir string) map[string]bool {
	ids := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(dir, "sessions")); err == nil {
		for id := range strings.FieldsSeq(string(data)) {
			ids[id] = true
		}
	}
	records, _ := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	for _, path := range records {
		var run struct {
			SessionID string `json:"session_id"`
		}
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &run) == nil && run.SessionID != "" {
			ids[run.SessionID] = true
		}
	}
	return ids
}

// startsNamed says whether a journal starts with a message that carries a
// title, as a /bg task's did.
func startsNamed(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("import: %v", err)
		}
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	if !scanner.Scan() {
		return false
	}
	var first struct {
		Type  string `json:"type"`
		Title string `json:"title"`
	}
	return json.Unmarshal(scanner.Bytes(), &first) == nil && first.Type == "message" && first.Title != ""
}
