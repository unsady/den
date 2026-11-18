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
	write(filepath.Join(cfg.State, "chat.json"), `{"session":"chat2","replies":[
		{"message":7,"session":"job1","label":"⏰ brief","choice":{"provider":"p","model":"m"}},
		{"message":8,"session":"gone"}]}`)

	db, err := store.Open(filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := importLegacy(ctx, cfg, db); err != nil {
		t.Fatal(err)
	}

	want := map[string]store.Session{
		"chat1": {Kind: store.Chat, Title: "Plan my week"},
		"chat2": {Kind: store.Chat},
		"bg1":   {Kind: store.Background, Title: "compare-models"},
		"job1":  {Kind: store.Job, Choice: store.Choice{Provider: "p", Model: "m"}},
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

	// A store that has sessions is not imported into again.
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
