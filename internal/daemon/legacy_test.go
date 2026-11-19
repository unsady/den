package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/store"
)

func TestImportLegacy(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{State: filepath.Join(dir, "state"), Sessions: filepath.Join(dir, "sessions")}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(cfg.Sessions, "index.jsonl"), `{"op":"put","id":"chat1","title":"Plan my","updated":3000}
{"op":"put","id":"chat2","title":"Hello","updated":2000}
{"op":"put","id":"bg1","title":"Compare","updated":1000}
{"op":"put","id":"job1","title":"Use the","updated":500}
{"op":"put","id":"sub","role":"subagent","updated":100}
`)
	journal := filepath.Join(cfg.State, "journal")
	write(filepath.Join(journal, "chat1.title"), "Plan my week")
	write(filepath.Join(journal, "bg1.jsonl"), `{"type":"message","title":"compare-models","text":"compare"}`+"\n")
	write(filepath.Join(journal, "bg1.title"), "compare-models")
	write(filepath.Join(cfg.State, "runs", "sessions"), "job1\n")
	write(filepath.Join(cfg.State, "chat.json"), `{"session":"chat2","offset":41,
		"provider":"p","model":"big","effort":"high","second":{"provider":"q","model":"fast"},
		"recent":["p/big","q/fast"],
		"inbox":[{"text":"hi"},{"text":"[den: done]","report":true,"notice":"done","session":"job1"},{"text":"lost","session":"gone"}],
		"replies":[
		{"message":7,"session":"job1","label":"⏰ brief","choice":{"provider":"p","model":"m"}},
		{"message":8,"session":"gone"}]}`)

	db, err := store.Open(filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// An import that failed partway left a session behind, which the next
	// one keeps.
	if err := db.AddSession(ctx, store.Session{ID: "chat1", Kind: store.KindChat, Title: "Plan my week"}); err != nil {
		t.Fatal(err)
	}
	if err := importLegacy(ctx, cfg, db); err != nil {
		t.Fatal(err)
	}

	want := map[string]store.Session{
		"chat1": {Kind: store.KindChat, Title: "Plan my week"},
		"chat2": {Kind: store.KindChat},
		"bg1":   {Kind: store.KindBackground, Title: "compare-models"},
		"job1":  {Kind: store.KindJob, Choice: store.Choice{Provider: "p", Model: "m"}},
	}
	list, _ := db.Sessions(ctx, store.Filter{})
	if len(list) != len(want) {
		t.Fatalf("imported %d sessions: %+v", len(list), list)
	}
	for _, s := range list {
		w := want[s.ID]
		if s.Kind != w.Kind || s.Title != w.Title || s.Choice != w.Choice {
			t.Errorf("%s: got %+v, want %+v", s.ID, s, w)
		}
	}
	if got, _ := db.Cursor(ctx, store.Telegram); got != "chat2" {
		t.Errorf("cursor %q", got)
	}
	if got, _ := db.TelegramSession(ctx, 7); got != "job1" {
		t.Errorf("message 7 in %q", got)
	}

	if c, _, _ := db.Model(ctx, store.Main); c != (store.Choice{Provider: "p", Model: "big", Effort: "high"}) {
		t.Errorf("main model %+v", c)
	}
	if c, _, _ := db.Model(ctx, store.Second); c != (store.Choice{Provider: "q", Model: "fast"}) {
		t.Errorf("second model %+v", c)
	}
	if recent, _ := db.RecentModels(ctx); len(recent) != 2 || recent[0].Model != "big" {
		t.Errorf("recent %+v", recent)
	}
	if n, _ := db.TelegramOffset(ctx); n != 41 {
		t.Errorf("offset %d", n)
	}
	// A message without a session goes to the chat's; one for a session fx
	// never saved is dropped.
	waiting, _ := db.Inbox(ctx)
	if len(waiting) != 2 || waiting[0].Session != "chat2" || !waiting[1].Report || waiting[1].Session != "job1" {
		t.Errorf("inbox %+v", waiting)
	}

	// The store is imported into once.
	if err := db.DeleteSession(ctx, "chat1"); err != nil {
		t.Fatal(err)
	}
	if err := importLegacy(ctx, cfg, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Session(ctx, "chat1"); err == nil {
		t.Error("a second import brought chat1 back")
	}
}

func TestImportLegacyJobs(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{State: filepath.Join(dir, "state")}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cfg.State, "jobs.json"), `[
		{"id":"66202d","name":"reflect","schedule":"0 4 * * *","prompt":"Use the reflect skill.","slot":"second","next_run":"2026-11-20T04:00:00Z"},
		{"id":"7fb4a6","name":"news-alerts","schedule":"10 * * * *","prompt":"Use the news skill.","last_status":"ok","last_run":"2026-11-19T01:10:00Z"},
		{"id":"a1b2c3","schedule":"in 30m","prompt":"Remind me.","once":true},
		{"id":"e8fe13","name":"Morning","schedule":"0 9 * * *","prompt":"Plan."}]`)
	write(filepath.Join(cfg.State, "default-jobs"), "reflect\ntidy\n")
	write(filepath.Join(cfg.State, "runs", "7fb4a6", "1.json"), `{"job_id":"7fb4a6","started":"2026-11-19T01:10:00Z","session_id":"s1","output":"news","delivered":true}`)
	write(filepath.Join(cfg.State, "runs", "gone", "1.json"), `{"job_id":"gone","started":"2026-11-18T01:00:00Z","error":"boom"}`)

	db, err := store.Open(filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.AddSession(ctx, store.Session{ID: "s1", Kind: store.KindJob}); err != nil {
		t.Fatal(err)
	}
	if err := importLegacyJobs(ctx, cfg, db); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		reflectJob:    "Reflect",
		"news-alerts": "News alerts",
		"older-job":   "Older job",
		"morning-job": "Morning job",
	}
	list, _ := db.Jobs(ctx)
	if len(list) != len(want) {
		t.Fatalf("jobs %+v", list)
	}
	for _, j := range list {
		if want[j.Name] != j.Title {
			t.Errorf("%s titled %q", j.Name, j.Title)
		}
	}
	if j, _ := db.Job(ctx, reflectJob); j.Slot != "second" || j.Prompt != "Use the reflect skill." {
		t.Errorf("reflect %+v", j)
	}
	if runs, _ := db.Runs(ctx, "news-alerts", 5); len(runs) != 1 || runs[0].Output != "news" || runs[0].SessionID != "s1" {
		t.Errorf("runs %+v", runs)
	}
	if runs, _ := db.Runs(ctx, "gone", 5); len(runs) != 1 || runs[0].Error != "boom" {
		t.Errorf("a removed job's runs %+v", runs)
	}
	if s, _ := db.Session(ctx, "s1"); s.Job != "news-alerts" {
		t.Errorf("session job %q", s.Job)
	}
	if v, _ := db.Setting(ctx, defaultsKey); v != reflectJob+" "+tidyJob {
		t.Errorf("defaults %q", v)
	}

	// The jobs are imported once.
	if _, err := db.RemoveJob(ctx, "news-alerts"); err != nil {
		t.Fatal(err)
	}
	if err := importLegacyJobs(ctx, cfg, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Job(ctx, "news-alerts"); err == nil {
		t.Error("a second import brought news-alerts back")
	}
}
