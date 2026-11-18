package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
	"github.com/unsady/den/internal/usage"
)

// answerTitles points the chat's questions to a model that gives answer,
// and counts how many it was asked.
func answerTitles(t *testing.T, answer string) *int {
	t.Helper()
	var asked int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
	}))
	t.Cleanup(server.Close)
	original := askURL
	askURL = func(provider, path string) string { return server.URL + "/" + provider + "/" + path }
	t.Cleanup(func() { askURL = original })
	return &asked
}

func newChat(t *testing.T) *Chat {
	t.Helper()
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	c.setMainModel(runner.Choice{Provider: "p1", Model: "auto"})
	return c
}

func addSession(t *testing.T, c *Chat, s store.Session) {
	t.Helper()
	if err := c.store.AddSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// TestNoLabelAfterAppMoves checks that a reply in Telegram's own session
// has no label, whatever the app does meanwhile: each client has its cursor.
func TestNoLabelAfterAppMoves(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	addSession(t, c, store.Session{ID: "other", Kind: store.Chat, Title: "Elsewhere"})

	telegram.message(42, "/new")
	telegram.expect(t, "✦ New session · <b>auto</b> · p1")
	telegram.message(42, "slow one")
	waitFor(t, func() bool { return c.Current() != "" })

	if err := c.Open(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if got := c.cursor(appClient); got != "other" {
		t.Fatalf("app cursor %q", got)
	}
	telegram.expect(t, "resume=none msg=slow one lines=1")
}

func TestReplyToJobSession(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	addSession(t, c, store.Session{ID: "job1", Kind: store.Job, Title: "news", Choice: store.Choice{Provider: "p2", Model: "m"}})
	c.Sent([]int64{900}, "job1")

	// A reply goes to the job's session, on its model, and says so.
	telegram.update(42, map[string]any{"text": "more", "reply_to_message": map[string]any{"message_id": 900}})
	telegram.expect(t, "⏰ news\n\nresume=job1 msg=more lines=1 provider=p2 model=m")
	if got := c.cursor(store.Telegram); got != "" {
		t.Fatalf("a reply to a job moved Telegram's cursor to %q", got)
	}

	// A message that replies to nothing goes to the cursor's session.
	telegram.message(42, "plain")
	telegram.expect(t, "resume=none msg=plain lines=1")
	if got := c.cursor(store.Telegram); got == "" || got == "job1" {
		t.Fatalf("cursor %q", got)
	}
}

func TestSessionTitle(t *testing.T) {
	asked := answerTitles(t, "\"Trip to Lisbon.\"")
	c := newChat(t)
	list, stop := c.Events.Listen()
	defer stop()
	ctx := context.Background()

	session, err := c.joined(store.Telegram)
	if err != nil {
		t.Fatal(err)
	}
	if !c.untitled(session) {
		t.Fatal("a new chat session has a title")
	}
	if s, _ := c.store.Session(ctx, session); s.Kind != store.Chat || s.Title != "" {
		t.Fatalf("new session %+v", s)
	}

	c.title(session, "plan a trip to Lisbon")
	if s, _ := c.store.Session(ctx, session); s.Title != "Trip to Lisbon" {
		t.Fatalf("title %q", s.Title)
	}
	select {
	case e := <-list:
		if e.Type != events.Titled || e.Session != session || e.Title != "Trip to Lisbon" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Titled event")
	}
	if c.untitled(session) {
		t.Fatal("still untitled")
	}

	// A title already set stays.
	if set, err := c.store.SetTitle(ctx, session, "Other"); err != nil || set {
		t.Fatalf("SetTitle replaced a title: %v, %v", set, err)
	}
	c.title(session, "something else")
	if s, _ := c.store.Session(ctx, session); s.Title != "Trip to Lisbon" {
		t.Fatalf("title %q", s.Title)
	}
	if *asked != 2 {
		t.Fatalf("asked %d times", *asked)
	}
}

func TestInvalidTitleAnswer(t *testing.T) {
	for _, answer := range []string{"One\nTwo", "", "  \n"} {
		answerTitles(t, answer)
		c := newChat(t)
		session, err := c.joined(store.Telegram)
		if err != nil {
			t.Fatal(err)
		}
		c.title(session, "hello")
		if !c.untitled(session) {
			t.Errorf("the answer %q titled the session", answer)
		}
	}
}

func TestTitleFromFirstMessage(t *testing.T) {
	answerTitles(t, "Greeting")
	_, telegram, c := startChatWith(t, nil)

	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	session := c.cursor(store.Telegram)
	waitFor(t, func() bool {
		s, err := c.store.Session(context.Background(), session)
		return err == nil && s.Title == "Greeting"
	})
}

func TestCleanTitle(t *testing.T) {
	long := ""
	for range 81 {
		long += "a"
	}
	for _, tc := range []struct {
		answer string
		want   string
		ok     bool
	}{
		{"Trip to Lisbon", "Trip to Lisbon", true},
		{"\"Trip to Lisbon.\"\n", "Trip to Lisbon", true},
		{"«Lightpanda release».", "Lightpanda release", true},
		{"  Spaced   out  title ", "Spaced out title", true},
		{"\n\nLeading blank lines\n\n", "Leading blank lines", true},
		{"Тестовый график", "Тестовый график", true},
		{"one two three four five six seven eight nine ten", "one two three four five six seven eight nine ten", true},
		{"one two three four five six seven eight nine ten eleven", "", false},
		{long, "", false},
		{"Two\nlines", "", false},
		{"", "", false},
		{" \n ", "", false},
		{"\"\"", "", false},
	} {
		got, ok := cleanTitle(tc.answer)
		if got != tc.want || ok != tc.ok {
			t.Errorf("cleanTitle(%q) = %q, %v, want %q, %v", tc.answer, got, ok, tc.want, tc.ok)
		}
	}
}

func TestForgetCursorSession(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)

	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	first := c.cursor(store.Telegram)
	if first == "" {
		t.Fatal("no cursor after a message")
	}
	// The reply is tied to its session once it is sent.
	waitFor(t, func() bool {
		_, err := c.store.TelegramSession(context.Background(), 7)
		return err == nil
	})

	if err := c.Forget(first); err != nil {
		t.Fatal(err)
	}
	if got := c.cursor(store.Telegram); got != "" {
		t.Fatalf("cursor %q after forgetting its session", got)
	}

	telegram.message(42, "again")
	telegram.expect(t, "resume=none msg=again lines=1")
	if second := c.cursor(store.Telegram); second == "" || second == first {
		t.Fatalf("cursor %q, want a new session", second)
	}
}
