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
			want := "crm: POST /api/deals\n" + map[string]string{Allowed: "✓ Allowed", Denied: "✗ Denied", Always: "✓ Always"}[outcome]
			if text, ok := a.Answer(id, outcome); !ok || text != want {
				t.Fatalf("answer found %v text %q", ok, text)
			}
			// However it was answered, the question shrinks to the answer.
			if got := <-edited; got != want {
				t.Fatalf("edited %q, want %q", got, want)
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

	// 1 hour allows the question's scope, and then den asks again.
	Hour = 100 * time.Millisecond
	scoped := Question{Text: "Post it?", What: "crm: POST /api/a", Scope: "fetch crm"}
	go a.Ask(context.Background(), scoped)
	got := <-sent
	if !strings.Contains(got, " hr:") {
		t.Fatalf("no 1 hour button: %q", got)
	}
	_, id, _ := strings.Cut(strings.Fields(got)[2], ":")
	a.Answer(id, ForHour)
	other := Question{Text: "Patch it?", What: "crm: PATCH /api/b/1", Scope: "fetch crm"}
	if err := a.Ask(context.Background(), other); err != nil {
		t.Fatalf("within the hour: %v", err)
	}
	select {
	case got := <-sent:
		t.Fatalf("asked within the hour: %q", got)
	default:
	}
	time.Sleep(150 * time.Millisecond)
	go a.Ask(context.Background(), other)
	if got := <-sent; !strings.HasPrefix(got, "Patch it?") {
		t.Fatalf("after the hour: %q", got)
	}

	// A question too long for one message is not asked, rather than cut.
	long := Question{Text: "Push to me/repo?\n\n• " + strings.Repeat("a", 5000) + "\n• main", What: "Push"}
	if err := a.Ask(context.Background(), long); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long: %v", err)
	}
}

func TestAnsweredNamesWhatAlwaysAndAnHourAllow(t *testing.T) {
	mail := Question{What: "Google: POST /gmail/v1/users/me/messages/send"}
	mail.MailTo("Anya@Example.com")
	fetch := Question{What: "crm: PATCH /api/deals/12", Label: "crm: PATCH /api/deals/*", ScopeLabel: "every write to crm but deletes"}
	smtp := Question{What: "Mail to anya@example.com"}
	smtp.MailTo("anya@example.com")

	for _, c := range []struct {
		q       Question
		outcome string
		want    string
	}{
		{mail, Always, "✓ Always: Mail to anya@example.com"},
		{mail, ForHour, "✓ For an hour: Mail to anya@example.com"},
		{fetch, Always, "✓ Always: crm: PATCH /api/deals/*"},
		{fetch, ForHour, "✓ For an hour: every write to crm but deletes"},
		{smtp, Always, "✓ Always"},
		{smtp, ForHour, "✓ For an hour"},
		{fetch, Allowed, "✓ Allowed"},
		{fetch, Denied, "✗ Denied"},
	} {
		if got := answered(c.q, c.outcome); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.q.What, c.outcome, got, c.want)
		}
	}
}

func TestAskWithFile(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
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
	ctx, stop := context.WithCancel(context.Background())
	q := Question{Text: "Post it?", What: "crm: POST /api/deals", File: []byte("{}"), FileName: "body.json"}
	waiting, err := a.Later(ctx, q, func(error) {})
	if err != nil || !waiting {
		t.Fatalf("Later = %v, %v", waiting, err)
	}

	// The question writes den's state once it ends, so it ends before the
	// test removes its folder.
	stop()
	a.Drain()
	if got := strings.Join(calls, " "); !strings.HasPrefix(got, "sendDocument sendMessage") {
		t.Errorf("the file goes before the question, got %s", got)
	}
}

func TestLaterAnswersRestartedWhenDenStops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	ctx, stop := context.WithCancel(context.Background())
	var answer error
	if _, err := a.Later(ctx, Question{Text: "Post it?", What: "crm: POST /api/deals"}, func(err error) { answer = err }); err != nil {
		t.Fatal(err)
	}
	stop()
	a.Drain()
	if !errors.Is(answer, ErrRestarted) {
		t.Errorf("answer = %v, want ErrRestarted", answer)
	}
}
