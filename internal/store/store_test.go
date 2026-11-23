package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), File))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSession(context.Background(), Session{ID: "a", Kind: KindChat}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Session(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
}

func TestTitleIsSetOnce(t *testing.T) {
	s, ctx := open(t), context.Background()
	if err := s.AddSession(ctx, Session{ID: "a", Kind: KindChat}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Session(ctx, "a")
	if got.Title != "" {
		t.Fatalf("a new session has title %q", got.Title)
	}
	if set, err := s.SetTitle(ctx, "a", "Trip to Lisbon"); err != nil || !set {
		t.Fatalf("first title: %v %v", set, err)
	}
	if set, _ := s.SetTitle(ctx, "a", "Other"); set {
		t.Fatal("a second title replaced the first")
	}
	got, _ = s.Session(ctx, "a")
	if got.Title != "Trip to Lisbon" {
		t.Fatalf("title %q", got.Title)
	}
}

func TestSessionsOrderAndFilter(t *testing.T) {
	s, ctx := open(t), context.Background()
	base := time.Now().Add(-time.Hour)
	for i, session := range []Session{
		{ID: "chat", Kind: KindChat, Title: "Taxes 2026"},
		{ID: "bg", Kind: KindBackground, Title: "Compare models"},
		{ID: "job", Kind: KindJob, Job: "j1", Title: "Morning brief"},
	} {
		session.Created = base.Add(time.Duration(i) * time.Minute)
		if err := s.AddSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Touch(ctx, "chat"); err != nil {
		t.Fatal(err)
	}

	list, err := s.Sessions(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := idsOf(list); ids != "chat job bg" {
		t.Fatalf("order %q", ids)
	}

	list, _ = s.Sessions(ctx, Filter{Kinds: []Kind{KindChat, KindBackground}})
	if ids := idsOf(list); ids != "chat bg" {
		t.Fatalf("kinds %q", ids)
	}

	list, _ = s.Sessions(ctx, Filter{Query: "taxes"})
	if ids := idsOf(list); ids != "chat" {
		t.Fatalf("query %q", ids)
	}

	list, _ = s.Sessions(ctx, Filter{Job: "j1"})
	if ids := idsOf(list); ids != "job" {
		t.Fatalf("job %q", ids)
	}

	// A page starts after the end of the one before.
	list, _ = s.Sessions(ctx, Filter{Limit: 2})
	last := list[len(list)-1]
	list, _ = s.Sessions(ctx, Filter{Before: &Mark{Updated: last.Updated, ID: last.ID}, Limit: 2})
	if ids := idsOf(list); ids != "bg" {
		t.Fatalf("next page %q", ids)
	}
}

func TestCursorsAndMessages(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.Cursor(ctx, Telegram); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty cursor: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.AddSession(ctx, Session{ID: id, Kind: KindChat}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetCursor(ctx, Telegram, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCursor(ctx, Telegram, "b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Cursor(ctx, Telegram); got != "b" {
		t.Fatalf("cursor %q", got)
	}

	if err := s.SentTelegram(ctx, "a", 10, 11); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TelegramSession(ctx, 11); got != "a" {
		t.Fatalf("message 11 in %q", got)
	}

	// Telegram's chat stays in every session it joined, twice is once.
	for _, id := range []string{"a", "a"} {
		if err := s.JoinTelegram(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if in, err := s.InTelegram(ctx, "a"); !in || err != nil {
		t.Fatalf("a in Telegram: %v %v", in, err)
	}
	if in, _ := s.InTelegram(ctx, "b"); in {
		t.Fatal("b in Telegram without joining")
	}

	// Deleting a session drops what points at it.
	if err := s.DeleteSession(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cursor(ctx, Telegram); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cursor after delete: %v", err)
	}
	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramSession(ctx, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message after delete: %v", err)
	}
	if in, _ := s.InTelegram(ctx, "a"); in {
		t.Fatal("deleted a still in Telegram")
	}
}

func TestNoSessionWithoutKind(t *testing.T) {
	s, ctx := open(t), context.Background()
	if err := s.AddSession(ctx, Session{ID: "a", Kind: "other"}); err == nil {
		t.Fatal("a session of an unknown kind was added")
	}
}

func idsOf(list []Session) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += " "
		}
		out += s.ID
	}
	return out
}

func TestArchive(t *testing.T) {
	s, ctx := open(t), context.Background()
	old := time.Now().Add(-100 * time.Hour)
	for _, session := range []Session{
		{ID: "old", Kind: KindChat, Created: old},
		{ID: "asking", Kind: KindChat, Created: old},
		{ID: "queued", Kind: KindChat, Created: old},
		{ID: "new", Kind: KindChat},
	} {
		if err := s.AddSession(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	// A session with a message in the inbox waits for its turn.
	if err := s.Enqueue(ctx, Message{Session: "queued", Text: "hi"}); err != nil {
		t.Fatal(err)
	}

	// Sessions unused since before the cutoff go, except those kept.
	ids, err := s.ArchiveIdle(ctx, time.Now().Add(-72*time.Hour), []string{"asking"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(ids); got != "[old]" {
		t.Fatalf("archived %s", got)
	}
	list, _ := s.Sessions(ctx, Filter{})
	if ids := idsOf(list); ids != "new queued asking" {
		t.Fatalf("feed %q", ids)
	}
	list, _ = s.Sessions(ctx, Filter{Shelf: Archive})
	if ids := idsOf(list); ids != "old" || !list[0].Archived {
		t.Fatalf("archive %q", ids)
	}
	list, _ = s.Sessions(ctx, Filter{Shelf: Everywhere})
	if ids := idsOf(list); ids != "new queued old asking" {
		t.Fatalf("everywhere %q", ids)
	}

	// A session used again comes back.
	if restored, err := s.Touch(ctx, "old"); err != nil || !restored {
		t.Fatalf("restored %v, %v", restored, err)
	}
	if restored, _ := s.Touch(ctx, "old"); restored {
		t.Fatal("restored a session out of the archive")
	}
	if list, _ = s.Sessions(ctx, Filter{Shelf: Archive}); len(list) != 0 {
		t.Fatalf("archive after use %q", idsOf(list))
	}

	if err := s.SetArchived(ctx, "new", true); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Sessions(ctx, Filter{Shelf: Archive}); idsOf(list) != "new" {
		t.Fatalf("archived by hand %q", idsOf(list))
	}

	// Taken out by hand, it is no longer idle, or ArchiveIdle would take it
	// back at once.
	if err := s.SetArchived(ctx, "asking", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArchived(ctx, "asking", false); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ArchiveIdle(ctx, time.Now().Add(-72*time.Hour), nil); slices.Contains(ids, "asking") {
		t.Fatal("archived again")
	}
	if err := s.SetArchived(ctx, "none", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
}
