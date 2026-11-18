package store

import (
	"context"
	"testing"
)

func TestInbox(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, id := range []string{"a", "b"} {
		if err := s.AddSession(ctx, Session{ID: id, Kind: Chat}); err != nil {
			t.Fatal(err)
		}
	}

	// A message for a session den does not keep is refused.
	if err := s.Enqueue(ctx, Message{Session: "gone", Text: "x"}); err == nil {
		t.Fatal("a message for no session was kept")
	}

	if err := s.Enqueue(ctx, Message{Session: "a", Text: "1"}, Message{Session: "b", Text: "2", App: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, Message{Session: "a", Text: "3", Report: true, Notice: "n"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Waiting(ctx); n != 3 {
		t.Fatalf("waiting %d", n)
	}

	// The session whose message waits longest goes first, with all of its.
	session, list, err := s.TakeTurn(ctx)
	if err != nil || session != "a" || len(list) != 2 || list[0].Text != "1" || !list[1].Report || list[1].Notice != "n" {
		t.Fatalf("turn %q %+v %v", session, list, err)
	}
	session, list, _ = s.TakeTurn(ctx)
	if session != "b" || len(list) != 1 || !list[0].App {
		t.Fatalf("turn %q %+v", session, list)
	}
	if session, list, err := s.TakeTurn(ctx); session != "" || list != nil || err != nil {
		t.Fatalf("empty inbox gave %q %+v %v", session, list, err)
	}

	// Deleting a session drops what waits for it; clearing drops the rest.
	if err := s.Enqueue(ctx, Message{Session: "a", Text: "4"}, Message{Session: "b", Text: "5"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	dropped, err := s.ClearInbox(ctx)
	if err != nil || len(dropped) != 1 || dropped[0].Text != "5" {
		t.Fatalf("cleared %+v %v", dropped, err)
	}
	if n, _ := s.Waiting(ctx); n != 0 {
		t.Fatalf("waiting %d after clear", n)
	}
}

func TestModels(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, ok, err := s.Model(ctx, Main); ok || err != nil {
		t.Fatalf("unset main: %v %v", ok, err)
	}
	if err := s.SetModel(ctx, Main, Choice{Provider: "p", Model: "m", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel(ctx, Main, Choice{Provider: "p", Model: "n"}); err != nil {
		t.Fatal(err)
	}
	if c, ok, _ := s.Model(ctx, Main); !ok || c != (Choice{Provider: "p", Model: "n"}) {
		t.Fatalf("main %+v", c)
	}
	if err := s.SetModel(ctx, "third", Choice{}); err == nil {
		t.Fatal("an unknown slot was kept")
	}
	if err := s.SetModel(ctx, Second, Choice{Provider: "q", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearModel(ctx, Second); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Model(ctx, Second); ok {
		t.Fatal("second kept after clear")
	}
}

func TestRecentModels(t *testing.T) {
	s, ctx := open(t), context.Background()
	for _, m := range []string{"a", "b", "a", "c"} {
		if err := s.UseModel(ctx, "p", m); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.RecentModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, c := range list {
		got += c.Model
	}
	if got != "cab" {
		t.Fatalf("recent %q", got)
	}
}

func TestSettingsAndContext(t *testing.T) {
	s, ctx := open(t), context.Background()
	if v, err := s.Setting(ctx, "k"); v != "" || err != nil {
		t.Fatalf("unset %q %v", v, err)
	}
	if err := s.SetSetting(ctx, "k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Setting(ctx, "k"); v != "2" {
		t.Fatalf("setting %q", v)
	}

	if err := s.AddSession(ctx, Session{ID: "a", Kind: Chat}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetContext(ctx, "a", 1200); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCompacted(ctx, "a", 90000); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Session(ctx, "a"); got.Context != 1200 || got.Compacted != 90000 {
		t.Fatalf("session %+v", got)
	}
}
