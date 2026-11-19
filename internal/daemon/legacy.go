package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
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
		session := store.Session{ID: s.ID, Kind: store.KindChat, Created: s.Updated, Updated: s.Updated, Choice: choices[s.ID]}
		switch {
		case ofJobs[s.ID]:
			session.Kind = store.KindJob
		case startsNamed(filepath.Join(journal, s.ID+".jsonl")):
			session.Kind = store.KindBackground
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

// jobsImportedKey marks a store that took an older den's jobs, and
// runsImportedKey one that took their runs.
const (
	jobsImportedKey = "legacy.jobs.imported"
	runsImportedKey = "legacy.runs.imported"
)

// legacyNames are the new names of den's own jobs.
var legacyNames = map[string]string{"reflect": reflectJob, "tidy": tidyJob}

// importLegacyJobs fills the store, once, with the jobs an older den kept
// in jobs.json, their runs in runs/, and which of den's own jobs it added.
// A job keeps its name when it is a valid one; one that is not gets one
// made from it. Its title is its name in words, which the user can change
// by adding the job again.
func importLegacyJobs(ctx context.Context, cfg config.Config, db *store.Store) error {
	if done, err := db.Setting(ctx, jobsImportedKey); err != nil || done != "" {
		return err
	}

	var old []struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		Schedule   string     `json:"schedule"`
		Prompt     string     `json:"prompt"`
		Slot       string     `json:"slot"`
		Silent     bool       `json:"silent"`
		Once       bool       `json:"once"`
		NextRun    time.Time  `json:"next_run"`
		Created    time.Time  `json:"created"`
		LastRun    *time.Time `json:"last_run"`
		LastStatus string     `json:"last_status"`
	}
	data, err := os.ReadFile(filepath.Join(cfg.State, "jobs.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("jobs.json: %w", err)
		}
	}

	names := map[string]string{}
	taken := map[string]bool{}
	for _, j := range old {
		name := legacyName(j.Name, j.ID, taken)
		taken[name] = true
		names[j.ID] = name
		title := legacyTitle(name)
		for _, d := range defaultJobs {
			if d.Name == name {
				title = d.Title
			}
		}
		err := db.AddJob(ctx, store.Job{
			Name: name, Title: title, Schedule: j.Schedule, Prompt: j.Prompt, Slot: j.Slot, Silent: j.Silent,
			Once: j.Once, NextRun: j.NextRun, Created: j.Created, LastRun: j.LastRun, LastStatus: j.LastStatus,
		})
		if err != nil && !errors.Is(err, store.ErrJobExists) {
			return err
		}
	}

	// The runs go in once, apart from the steps after them, so a failure
	// later on does not import them again.
	runs := 0
	if done, err := db.Setting(ctx, runsImportedKey); err != nil {
		return err
	} else if done == "" {
		records, _ := filepath.Glob(filepath.Join(cfg.State, "runs", "*", "*.json"))
		for _, path := range records {
			var run struct {
				store.Run
				JobID string `json:"job_id"`
			}
			data, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(data, &run) != nil {
				continue
			}
			name, ok := names[run.JobID]
			if !ok {
				// The job was removed; its runs keep its id for a name.
				name = run.JobID
			}
			run.Run.Job = name
			if err := db.AddRun(ctx, run.Run); err != nil {
				return err
			}
			if run.SessionID != "" {
				if err := db.SetSessionJob(ctx, run.SessionID, name); err != nil {
					return err
				}
			}
			runs++
		}
		if err := db.SetSetting(ctx, runsImportedKey, "1"); err != nil {
			return err
		}
	}

	// den's own jobs it added, by their new names.
	var added []string
	if data, err := os.ReadFile(filepath.Join(cfg.State, "default-jobs")); err == nil {
		for _, name := range strings.Fields(string(data)) {
			if n, ok := legacyNames[name]; ok {
				added = append(added, n)
			}
		}
	} else if len(old) > 0 {
		// den added reflect with the jobs before it kept the list.
		added = append(added, reflectJob)
	}
	if len(added) > 0 {
		if err := db.SetSetting(ctx, defaultsKey, strings.Join(added, " ")); err != nil {
			return err
		}
	}

	log.Printf("import: %d jobs, %d runs", len(old), runs)
	return db.SetSetting(ctx, jobsImportedKey, "1")
}

// legacyName is the name an older den's job takes: den's own jobs' new
// names, the job's name when it is a valid one, or one made of its words,
// with its id spelled in letters when that one is taken.
func legacyName(name, id string, taken map[string]bool) string {
	if n, ok := legacyNames[name]; ok {
		return n
	}
	if jobs.CheckName(name) == nil && !taken[name] {
		return name
	}

	var words []string
	for w := range strings.FieldsFuncSeq(strings.ToLower(name), func(r rune) bool { return r < 'a' || r > 'z' }) {
		words = append(words, w)
	}
	switch len(words) {
	case 0:
		words = []string{"older", "job"}
	case 1:
		words = append(words, "job")
	}
	base := strings.Join(words[:2], "-")
	if !taken[base] {
		return base
	}
	letters := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return 'a' + (r - '0')
		}
		return r
	}, strings.ToLower(id))
	return base + "-" + letters
}

// legacyTitle writes a job's name in words: news-alerts is "News alerts".
func legacyTitle(name string) string {
	title := strings.ReplaceAll(name, "-", " ")
	return strings.ToUpper(title[:1]) + title[1:]
}
