package approve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/notify"
)

func TestAsk(t *testing.T) {
	sent := make(chan string, 10)
	edited := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var data []string
			for _, row := range body["reply_markup"].(map[string]any)["inline_keyboard"].([]any) {
				for _, b := range row.([]any) {
					data = append(data, b.(map[string]any)["callback_data"].(string))
				}
			}
			sent <- body["text"].(string) + " " + strings.Join(data, " ")
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			edited <- body["text"].(string)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5}})
	}))
	defer server.Close()
	notify.API = server.URL

	dir := t.TempDir()
	cfg := config.Config{Secrets: dir, State: dir}
	for name, value := range map[string]string{"telegram_token": "token", "telegram_chat_id": "42"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(cfg)
	q := Question{Text: "Post it?", What: "crm: POST /api/deals", Rule: "fetch crm POST /api/deals"}

	ask := func(outcome string) error {
		done := make(chan error, 1)
		go func() { done <- a.Ask(context.Background(), q) }()
		got := <-sent
		if !strings.HasSuffix(got, " al:"+strings.TrimPrefix(strings.Fields(got)[2], "ok:")) {
			t.Fatalf("a question with a rule offers Always: %q", got)
		}
		if outcome != "" {
			_, id, _ := strings.Cut(strings.Fields(got)[2], ":")
			if text, ok := a.Answer(id, outcome); !ok || text != "crm: POST /api/deals" {
				t.Fatalf("answer found %v text %q", ok, text)
			}
		}
		return <-done
	}

	if err := ask(Allowed); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if err := ask(Denied); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied: %v", err)
	}

	Wait = 50 * time.Millisecond
	if err := ask(""); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if got := <-edited; got != "crm: POST /api/deals\n✗ Expired" {
		t.Fatalf("edited %q", got)
	}
	if _, ok := a.Answer("3", Allowed); ok {
		t.Fatal("an expired question took an answer")
	}

	// Always allows this kind of request from now on, without a question.
	if err := ask(Always); err != nil {
		t.Fatalf("always: %v", err)
	}
	if err := a.Ask(context.Background(), q); err != nil {
		t.Fatalf("by rule: %v", err)
	}
	select {
	case got := <-sent:
		t.Fatalf("asked again: %q", got)
	default:
	}
	a.Note("Push to me/notes: work", Went)

	var outcomes []string
	for _, act := range a.Actions(time.Now().Add(-time.Hour)) {
		outcomes = append(outcomes, act.Outcome)
	}
	if got, want := strings.Join(outcomes, ","), "allowed by you,denied,expired,allowed for good,allowed by your rule,went"; got != want {
		t.Fatalf("log %s, want %s", got, want)
	}

	rules := a.Rules()
	if len(rules) != 1 || rules[0].Label != "crm: POST /api/deals" {
		t.Fatalf("rules %+v", rules)
	}
	if _, ok := a.Forget(rules[0].ID()); !ok || len(a.Rules()) != 0 {
		t.Fatal("forget kept the rule")
	}

	// Without a rule, a question offers no Always.
	go a.Ask(context.Background(), Question{Text: "Mail it?", What: "Mail to a@example.com"})
	if got := <-sent; strings.Contains(got, "al:") {
		t.Fatalf("mail offered Always: %q", got)
	}

	// Later returns at once and passes the answer on when it comes.
	later := func(outcome string) (bool, error) {
		answered := make(chan error, 1)
		waiting, err := a.Later(context.Background(), q, func(err error) { answered <- err })
		if err != nil || !waiting {
			return waiting, err
		}
		_, id, _ := strings.Cut(strings.Fields(<-sent)[2], ":")
		if _, ok := a.Answer(id, outcome); !ok {
			t.Fatal("later: the question was not waiting")
		}
		return true, <-answered
	}
	if waiting, err := later(Denied); !waiting || !errors.Is(err, ErrDenied) {
		t.Fatalf("later denied: %v %v", waiting, err)
	}
	if waiting, err := later(Always); !waiting || err != nil {
		t.Fatalf("later always: %v %v", waiting, err)
	}
	if waiting, err := later(""); waiting || err != nil {
		t.Fatalf("later by rule: %v %v", waiting, err)
	}
}
