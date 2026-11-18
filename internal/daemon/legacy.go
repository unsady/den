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

// importLegacy fills an empty store from the files an older den kept about
// its sessions: fx's list of them, the titles den made in the journal, the
// job runs' sessions, and the chat's session and the bot's messages in
// chat.json. A store that has sessions is left as it is.
func importLegacy(ctx context.Context, cfg config.Config, db *store.Store) error {
	if have, err := db.Sessions(ctx, store.Filter{Limit: 1}); err != nil || len(have) > 0 {
		return err
	}
	list, err := sessions.List(cfg.Sessions, 1<<20)
	if err != nil || len(list) == 0 {
		return err
	}

	var chat struct {
		Session string `json:"session"`
		Replies []struct {
			Message int64  `json:"message"`
			Session string `json:"session"`
			Choice  *struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Effort   string `json:"effort"`
			} `json:"choice"`
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
		if err := db.AddSession(ctx, session); err != nil {
			return err
		}
		imported[s.ID] = true
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
	log.Printf("import: %d sessions", len(imported))
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
