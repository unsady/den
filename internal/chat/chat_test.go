package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/mapper"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
	"github.com/unsady/den/internal/usage"
)

// fakeFX runs fx acp as fakeACP, and answers fx's quick commands.
const fakeFX = `#!/bin/sh
case "$1" in
acp) DEN_FAKE_ACP=1 exec "$DEN_TEST_BINARY" "$@" ;;
status) printf '{"model_source":"%s","model":"auto","connected_providers":["p1","p2"]}\n' "${FX_PROVIDER:-p1}"; exit ;;
models) printf '{"ids":["auto","fast-%s","big-%s"]}\n' "${FX_PROVIDER:-p1}" "${FX_PROVIDER:-p1}"; exit ;;
esac
echo "fake fx: $1 is not faked" >&2
exit 2
`

type fakeTelegram struct {
	mu      sync.Mutex
	pending []map[string]any
	next    int64
	sent    chan string
	menu    []any
	// deleted receives the ids of deleted messages.
	deleted chan int64
	// edited receives the new text of each edited message.
	edited chan string
	// answered receives the text of each answered button press.
	answered chan string
	// reacted holds the messages that got a reaction, as "id emoji".
	reacted []string
	// buttons holds the callback data of the last message's buttons.
	buttons []string
	// labels are the texts of those buttons.
	labels []string
}

// press queues a press of a button with the given data.
func (f *fakeTelegram) press(data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.pending = append(f.pending, map[string]any{"update_id": f.next, "callback_query": map[string]any{
		"id":      fmt.Sprint(f.next),
		"data":    data,
		"message": map[string]any{"chat": map[string]any{"id": 42}, "message_id": 1},
	}})
}

func (f *fakeTelegram) message(chat int64, text string) {
	f.update(chat, map[string]any{"text": text})
}

// update queues a message with the given fields and returns its id.
func (f *fakeTelegram) update(chat int64, message map[string]any) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	message["chat"] = map[string]any{"id": chat}
	message["message_id"] = f.next
	f.pending = append(f.pending, map[string]any{"update_id": f.next, "message": message})
	return f.next
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/audio/transcriptions" {
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		audio, _ := io.ReadAll(file)
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "heard " + string(audio)})
		return
	}
	if file, ok := strings.CutPrefix(r.URL.Path, "/file/bottoken/files/"); ok {
		_, _ = w.Write([]byte("content of " + file))
		return
	}

	// Files come as a form, and the fake tells which, in name order.
	switch method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]; method {
	case "sendPhoto", "sendDocument", "sendMediaGroup":
		_ = r.ParseMultipartForm(1 << 20)
		var names []string
		var result []map[string]any
		for _, files := range r.MultipartForm.File {
			for _, file := range files {
				names = append(names, file.Filename)
				result = append(result, map[string]any{"message_id": 8})
			}
		}
		slices.Sort(names)
		f.sent <- "📎 " + method + " " + strings.Join(names, " ")
		if method == "sendMediaGroup" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result[0]})
		}
		return
	}

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	var result any = true
	switch {
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		deadline := time.Now().Add(time.Second)
		for {
			f.mu.Lock()
			updates := f.pending
			f.pending = nil
			f.mu.Unlock()
			if len(updates) > 0 || time.Now().After(deadline) {
				result = updates
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	case strings.HasSuffix(r.URL.Path, "/setMyCommands"):
		f.mu.Lock()
		f.menu, _ = body["commands"].([]any)
		f.mu.Unlock()
	case strings.HasSuffix(r.URL.Path, "/sendMessage"), strings.HasSuffix(r.URL.Path, "/editMessageText"):
		var buttons, labels []string
		if markup, ok := body["reply_markup"].(map[string]any); ok {
			for _, row := range markup["inline_keyboard"].([]any) {
				for _, b := range row.([]any) {
					buttons = append(buttons, b.(map[string]any)["callback_data"].(string))
					labels = append(labels, b.(map[string]any)["text"].(string))
				}
			}
		}
		f.mu.Lock()
		f.buttons, f.labels = buttons, labels
		f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			f.edited <- fmt.Sprintf("%v: %s", body["message_id"], body["text"])
			break
		}
		f.sent <- body["text"].(string)
		result = map[string]any{"message_id": 7}
	case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		f.answered <- body["text"].(string)
	case strings.HasSuffix(r.URL.Path, "/getFile"):
		result = map[string]any{"file_path": "files/" + body["file_id"].(string)}
	case strings.HasSuffix(r.URL.Path, "/setMessageReaction"):
		emoji := body["reaction"].([]any)[0].(map[string]any)["emoji"]
		f.mu.Lock()
		f.reacted = append(f.reacted, fmt.Sprintf("%v %v", body["message_id"], emoji))
		f.mu.Unlock()
	case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
		f.deleted <- int64(body["message_id"].(float64))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// lastMessage is the user's latest message in session's journal.
func lastMessage(t *testing.T, cfg config.Config, session string) events.Event {
	t.Helper()
	list, err := events.Journal(filepath.Join(cfg.State, "journal"), session)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Type == events.Message {
			return list[i]
		}
	}
	t.Fatalf("no message in %s", session)
	return events.Event{}
}

func (f *fakeTelegram) expect(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.sent:
		if got != want {
			t.Fatalf("sent %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing sent, want %q", want)
	}
}

// expectAll waits for each of want to be sent, in any order.
func (f *fakeTelegram) expectAll(t *testing.T, want ...string) {
	t.Helper()
	left := slices.Clone(want)
	for len(left) > 0 {
		select {
		case got := <-f.sent:
			i := slices.Index(left, got)
			if i < 0 {
				t.Fatalf("sent %q, want one of %q", got, left)
			}
			left = slices.Delete(left, i, i+1)
		case <-time.After(5 * time.Second):
			t.Fatalf("nothing sent, want %q", left)
		}
	}
}

func (f *fakeTelegram) expectEdit(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.edited:
		if got != want {
			t.Fatalf("edited %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing edited, want %q", want)
	}
}

func (f *fakeTelegram) expectButtons(t *testing.T, want ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.buttons, " ") != strings.Join(want, " ") {
		t.Fatalf("buttons %v, want %v", f.buttons, want)
	}
}

func (f *fakeTelegram) expectLabels(t *testing.T, want ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.labels, "|") != strings.Join(want, "|") {
		t.Fatalf("labels %q, want %q", f.labels, want)
	}
}

func (f *fakeTelegram) expectAnswer(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.answered:
		if got != want {
			t.Fatalf("answered %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no answer, want %q", want)
	}
}

// startChat runs a chat against a fake Telegram and a fake fx until the
// test ends.
func startChat(t *testing.T, den Daemon) (config.Config, *fakeTelegram) {
	t.Helper()
	cfg, telegram, _ := startChatWith(t, den)
	return cfg, telegram
}

// startChatWith is startChat that also returns the running chat. Its new
// sessions are s1, s2, and so on.
func startChatWith(t *testing.T, den Daemon) (config.Config, *fakeTelegram, *Chat) {
	t.Helper()
	countSessions(t)
	dir := t.TempDir()
	cfg := config.Config{
		State:      filepath.Join(dir, "state"),
		Secrets:    filepath.Join(dir, "secrets"),
		Space:      dir,
		Inbox:      filepath.Join(dir, "inbox"),
		FX:         filepath.Join(dir, "fx"),
		Sessions:   filepath.Join(dir, "sessions"),
		JobTimeout: 10 * time.Second,
	}
	for name, value := range map[string]string{
		cfg.FX: fakeFX,
		filepath.Join(cfg.Secrets, "telegram_token"):   "token",
		filepath.Join(cfg.Secrets, "telegram_chat_id"): "42",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(value), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(cfg.State, 0o755); err != nil {
		t.Fatal(err)
	}

	progressEvery = 100 * time.Millisecond
	telegram := &fakeTelegram{sent: make(chan string, 10), edited: make(chan string, 10), deleted: make(chan int64, 10), answered: make(chan string, 10)}
	server := httptest.NewServer(telegram)
	t.Cleanup(server.Close)
	notify.API = server.URL
	cfg.TranscribeURL = server.URL + "/v1/audio/transcriptions"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c := New(cfg, den, usage.Open(cfg.State), testStore(t, cfg))
	go func() {
		c.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	stops.Store(c, cancel)
	return cfg, telegram, c
}

// stops holds what stops each test's chat, as den's stopping does.
var stops sync.Map

// stopChat stops c as den does once its wait for running turns is over.
func stopChat(c *Chat) {
	if cancel, ok := stops.Load(c); ok {
		cancel.(context.CancelFunc)()
	}
}

// countSessions names the new sessions s1, s2, and so on until the test ends.
func countSessions(t *testing.T) {
	original, n := newSession, 0
	newSession = func() string {
		n++
		return fmt.Sprintf("s%d", n)
	}
	t.Cleanup(func() { newSession = original })
}

func TestChat(t *testing.T) {
	cfg, telegram, c := startChatWith(t, nil)

	telegram.message(7, "from a stranger")
	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	prompt, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
	if strings.TrimSpace(string(prompt)) != "hello" {
		t.Fatalf("first prompt %q is not the user's words alone", prompt)
	}

	telegram.mu.Lock()
	menu := telegram.menu
	telegram.mu.Unlock()
	if len(menu) != len(commands) || menu[0].(map[string]any)["command"] != "new" {
		t.Fatalf("command menu = %v", menu)
	}

	telegram.message(42, "again")
	telegram.expect(t, "resume=s1 msg=again lines=1")

	telegram.message(42, "/new@den_bot")
	telegram.expect(t, "✦ New session · <b>auto</b> · p1")
	// A reply to a message of the old session goes on in that session,
	// and the chat stays in the new one.
	telegram.update(42, map[string]any{
		"text":             "more",
		"reply_to_message": map[string]any{"message_id": 7, "text": "resume=s1 msg=again lines=1", "from": map[string]any{"is_bot": true}},
	})
	telegram.expect(t, "↩ Earlier session\n\nresume=s1 msg=more lines=2")
	telegram.message(42, "fresh")
	telegram.expect(t, "resume=none msg=fresh lines=1")

	telegram.message(42, "/stop")
	telegram.expect(t, "Nothing is running.")

	// A turn shows the steps fx reports, with what the agent said before
	// the latest, then deletes them before replying.
	telegram.message(42, "steps")
	telegram.expect(t, "Working…\n• Read a.txt\n→ Running ls ×2…\n   Listing the folder.")
	select {
	case id := <-telegram.deleted:
		if id != 7 {
			t.Fatalf("deleted message %d, want 7", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the progress message was not deleted")
	}
	telegram.expect(t, "resume=s2 msg=steps lines=1")

	// A photo is saved, named in the turn, and attached.
	day := filepath.Join(cfg.Inbox, time.Now().Format("2006-01-02"))
	id := telegram.update(42, map[string]any{
		"caption": "what is this",
		"photo":   []any{map[string]any{"file_id": "small"}, map[string]any{"file_id": "large"}},
	})
	photo := filepath.Join(day, fmt.Sprintf("%d-photo.jpg", id))
	telegram.expect(t, "resume=s2 msg=what is this lines=2 images=1")
	expectFile(t, photo, "content of large")

	// The app gets the caption with the photo kept for it.
	if e := lastMessage(t, cfg, "s2"); e.Text != "what is this" || len(e.Files) != 1 || e.Files[0].Name != "photo.jpg" {
		t.Fatalf("message %+v", e)
	} else {
		expectFile(t, c.Events.FilePath("s2", e.Files[0].File), "content of large")
	}

	// A model that cannot see it gets the message without it.
	telegram.update(42, map[string]any{
		"caption": "blind look",
		"photo":   []any{map[string]any{"file_id": "large"}},
	})
	telegram.expect(t, "resume=s2 msg=blind look lines=3")

	// A document keeps a safe form of its name and is not attached.
	id = telegram.update(42, map[string]any{
		"document": map[string]any{"file_id": "doc", "file_name": "../.Report v1.pdf"},
	})
	document := filepath.Join(day, fmt.Sprintf("%d-Report_v1.pdf", id))
	telegram.expect(t, "resume=s2 msg=[The user sent a file, saved at "+document+"] lines=1")
	expectFile(t, document, "content of doc")
	if e := lastMessage(t, cfg, "s2"); e.Text != "" || len(e.Files) != 1 || e.Files[0].Name != "../.Report v1.pdf" {
		t.Fatalf("message %+v", e)
	}

	// The files of an album reach the agent as one message.
	first := telegram.update(42, map[string]any{
		"media_group_id": "album",
		"document":       map[string]any{"file_id": "doc", "file_name": "a.md"},
	})
	telegram.update(42, map[string]any{
		"media_group_id": "album",
		"document":       map[string]any{"file_id": "doc", "file_name": "b.csv"},
	})
	// Both files are in the one turn: two lines, not one turn each.
	telegram.expect(t, fmt.Sprintf("resume=s2 msg=[The user sent a file, saved at %s] lines=2", filepath.Join(day, fmt.Sprintf("%d-a.md", first))))

	telegram.update(42, map[string]any{"sticker": map[string]any{"file_id": "s"}})
	telegram.expect(t, "den reads text, photos, voice messages, and files for now.")

	// A voice message comes with its transcript.
	id = telegram.update(42, map[string]any{"voice": map[string]any{"file_id": "talk"}})
	telegram.expect(t, "resume=s2 msg=heard content of talk lines=2")
	prompt, _ = os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
	if !strings.HasPrefix(string(prompt), "heard content of talk\n[The user said this in a voice message") {
		t.Fatalf("voice prompt %q", prompt)
	}
	expectFile(t, filepath.Join(day, fmt.Sprintf("%d-voice.ogg", id)), "content of talk")

	// Messages sent during a turn join it, each marked with ✍; a message
	// that starts a turn is not marked.
	slow := telegram.update(42, map[string]any{"text": "slow one"})
	time.Sleep(300 * time.Millisecond)
	a := telegram.update(42, map[string]any{"text": "a"})
	b := telegram.update(42, map[string]any{"text": "b"})
	telegram.expect(t, "resume=s2 msg=slow one lines=1 steered=a steered=b")
	telegram.mu.Lock()
	reacted := strings.Join(telegram.reacted, ", ")
	telegram.mu.Unlock()
	if want := fmt.Sprintf("%d ✍, %d ✍", a, b); reacted != want {
		t.Fatalf("reacted %q, want %q (slow one is %d)", reacted, want, slow)
	}

	// /stop ends the running turn without a reply.
	telegram.message(42, "slow two")
	time.Sleep(300 * time.Millisecond)
	telegram.message(42, "/stop")
	telegram.expect(t, "Stopped.")
	time.Sleep(1500 * time.Millisecond)

	select {
	case got := <-telegram.sent:
		t.Fatalf("unexpected message %q", got)
	default:
	}

	if got, err := testStore(t, cfg).TelegramOffset(context.Background()); err != nil || got != 21 {
		t.Fatalf("offset %d, %v; want 21", got, err)
	}
}

// inbox lists what waits in c's inbox, as text@session.
func inbox(t *testing.T, c *Chat) []string {
	t.Helper()
	list, err := c.store.Inbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range list {
		out = append(out, m.Text+"@"+m.Session)
	}
	return out
}

func expectFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s holds %q, want %q", path, data, want)
	}
}

type fakeDaemon struct {
	mu   sync.Mutex
	list []jobs.Job
	// questions maps a waiting question's id to its text, and answers
	// records each answer as id=true or id=false.
	questions map[string]string
	answers   []string
	// rules and actions are what /log shows.
	rules   []approve.Rule
	actions []approve.Action
	// jobSessions are the sessions of job runs.
	jobSessions map[string]bool
}

func (f *fakeDaemon) JobSessions() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobSessions
}

func (f *fakeDaemon) Answer(id, outcome string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text, found := f.questions[id]
	if found {
		delete(f.questions, id)
		f.answers = append(f.answers, fmt.Sprintf("%s=%s", id, outcome))
		text += "\n" + map[string]string{approve.Denied: "✗ Denied", approve.Always: "✓ Always"}[outcome]
	}
	return text, found
}

func (f *fakeDaemon) Rules() []approve.Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]approve.Rule(nil), f.rules...)
}

func (f *fakeDaemon) Forget(id string) (approve.Rule, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rules {
		if r.ID() == id {
			f.rules = append(f.rules[:i:i], f.rules[i+1:]...)
			return r, true
		}
	}
	return approve.Rule{}, false
}

func (f *fakeDaemon) Actions(since time.Time) []approve.Action { return f.actions }

func (f *fakeDaemon) ListJobs() []jobs.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jobs.Job(nil), f.list...)
}

func (f *fakeTelegram) expectPrefix(t *testing.T, want string) string {
	t.Helper()
	select {
	case got := <-f.sent:
		if !strings.HasPrefix(got, want) {
			t.Fatalf("sent %q, want it to start with %q", got, want)
		}
		return got
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing sent, want %q…", want)
	}
	return ""
}

func TestChatControls(t *testing.T) {
	next := time.Date(2026, 11, 10, 4, 0, 0, 0, time.Local)
	scheduler := &fakeDaemon{questions: map[string]string{"1": "Send it?", "2": "Push it?"}, list: []jobs.Job{{Name: "nightly-reflect", Title: "Reflect", Schedule: "0 4 * * *", Prompt: "Use the reflect skill.", NextRun: next}}}
	cfg, telegram, c := startChatWith(t, scheduler)

	now := time.Now()
	seed(t, c,
		store.Session{ID: "aaa", Kind: store.KindChat, Title: "Taxes", Updated: now.Add(-2 * time.Hour)},
		store.Session{ID: "bbb", Kind: store.KindChat, Title: "Trip", Updated: now.Add(-time.Hour)},
	)

	// /sessions lists chat sessions, newest first, and a button switches.
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions:")
	telegram.expectButtons(t, "s:bbb", "s:aaa", "sj:", "x:")
	// ✕ deletes the list.
	telegram.press("x:")
	telegram.expectAnswer(t, "")
	if id := <-telegram.deleted; id != 1 {
		t.Fatalf("deleted %d", id)
	}
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions:")
	telegram.expectButtons(t, "s:bbb", "s:aaa", "sj:", "x:")
	telegram.press("s:aaa")
	telegram.expectEdit(t, "1: ↺ Taxes · <b>auto</b> · p1")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t)
	telegram.message(42, "hi")
	telegram.expect(t, "resume=aaa msg=hi lines=1")

	// A reply carries the part of the message it quotes.
	telegram.update(42, map[string]any{
		"text":             "redo this",
		"reply_to_message": map[string]any{"text": "Here is the plan", "from": map[string]any{"is_bot": true}},
	})
	telegram.expect(t, "resume=aaa msg=redo this lines=2")
	prompt, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
	if want := "redo this\n[In reply to your earlier message: «Here is the plan»]"; string(prompt) != want {
		t.Fatalf("prompt %q, want %q", prompt, want)
	}

	// /jobs lists the jobs; the agent changes and runs them.
	telegram.message(42, "/jobs")
	telegram.expect(t, "Jobs:\n\n<b>Reflect</b> · next Tue 10 Nov 04:00\nUse the reflect skill.\n\nAsk the agent to change or run a job.")
	telegram.expectButtons(t, "x:")

	// /model offers the current and recent models, the full list by
	// provider, and a search. Pressing changes the list in place, and a
	// model turns it into one line.
	telegram.message(42, "/model")
	telegram.expect(t, "⚙︎ <b>auto</b> · p1\nSecond: none\n\nOr find one by name: /model deep")
	telegram.expectButtons(t, "m:p1/auto", "2:", "a:p1:0", "x:")
	telegram.press("a:p2:0")
	telegram.expectEdit(t, "1: ⚙︎ <b>auto</b> · p1\n\np2 has 3 models.")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "a:p1:0", "a:p2:0", "m:p2/auto", "m:p2/fast-p2", "m:p2/big-p2", "r:", "x:")
	telegram.press("m:p2/big-p2")
	telegram.expectEdit(t, "1: ⚙︎ <b>big-p2</b> · p2")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t)
	telegram.message(42, "/model nope")
	telegram.expect(t, "No model matches «nope». Send /model to see them all.")
	telegram.message(42, "/model fast")
	telegram.expect(t, "Models with «fast»:")
	telegram.expectButtons(t, "m:p2/fast-p2", "m:p1/fast-p1", "x:")
	telegram.message(42, "/model big p1")
	telegram.expect(t, "⚙︎ <b>big-p1</b> · p1")
	telegram.message(42, "/model")
	telegram.expect(t, "⚙︎ <b>big-p1</b> · p1\nSecond: none\n\nOr find one by name: /model deep")
	telegram.expectButtons(t, "m:p1/big-p1", "m:p2/big-p2", "2:", "a:p1:0", "x:")
	telegram.message(42, "/model big-p2")
	telegram.expect(t, "⚙︎ <b>big-p2</b> · p2")

	// /effort offers the levels fx's catalog gives the model, and a model
	// without the level chosen goes back to its default.
	catalogs := filepath.Join(filepath.Dir(filepath.Dir(cfg.Sessions)), "model-catalogs")
	os.MkdirAll(catalogs, 0o755)
	os.WriteFile(filepath.Join(catalogs, "p2.json"), []byte(`{"big-p2":{"reasoning_options":[{"type":"effort","values":["low","high"]}]}}`), 0o644)
	telegram.message(42, "/effort")
	telegram.expect(t, "⚙︎ <b>big-p2</b> · p2\n\nHow hard it thinks:")
	telegram.expectButtons(t, "e:default", "e:low", "e:high", "x:")
	telegram.press("e:high")
	telegram.expectEdit(t, "1: ⚙︎ <b>big-p2</b> · p2 · high")
	telegram.expectAnswer(t, "")
	telegram.message(42, "/effort max")
	telegram.expect(t, "big-p2 takes the effort low, high, or default.")

	// The second model is chosen from the recent ones, with its own effort.
	telegram.message(42, "/model")
	telegram.expectPrefix(t, "⚙︎ <b>big-p2</b> · p2\nSecond: none")
	telegram.press("2:")
	telegram.expectEdit(t, "1: ⚙︎ Second: none\n\nJobs on the second model use it too.")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "n:p2/big-p2", "n:p1/big-p1", "n:", "r:", "A:p2:0", "x:")
	telegram.press("n:p2/big-p2")
	telegram.expectEdit(t, "1: ⚙︎ Second: <b>big-p2</b> · p2")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "f:default", "f:low", "f:high")
	telegram.press("f:low")
	telegram.expectEdit(t, "1: ⚙︎ Second: <b>big-p2</b> · p2 · low")
	telegram.expectAnswer(t, "")
	telegram.message(42, "/model")
	telegram.expectPrefix(t, "⚙︎ <b>big-p2</b> · p2\nSecond: <b>big-p2</b> · p2 · low")
	telegram.message(42, "hey")
	telegram.expect(t, "resume=aaa msg=hey lines=1 provider=p2 model=big-p2 effort=high")

	telegram.message(42, "/new")
	telegram.expect(t, "✦ New session · <b>big-p2</b> · p2 · high")
	telegram.message(42, "/effort default")
	telegram.expect(t, "⚙︎ <b>big-p2</b> · p2")
	telegram.message(42, "/new")
	telegram.expect(t, "✦ New session · <b>big-p2</b> · p2")
	telegram.message(42, "/model default")
	telegram.expect(t, "⚙︎ <b>auto</b> · p1")
	telegram.message(42, "/effort")
	telegram.expect(t, "⚙︎ <b>auto</b> · p1 has no effort levels.")
	telegram.message(42, "/new please")
	telegram.expect(t, "resume=none msg=/new please lines=1")

	// A button answers a question den asked, once; den shrinks it.
	telegram.press("no:1")
	telegram.expectAnswer(t, "")
	telegram.press("ok:1")
	telegram.expectAnswer(t, "This question has expired.")
	telegram.press("al:2")
	telegram.expectAnswer(t, "")
	if strings.Join(scheduler.answers, ",") != "1=denied,2=allowed for good" {
		t.Fatalf("answers %v", scheduler.answers)
	}

	// /log lists the day's actions and the rules, each with a button that
	// forgets it.
	at := time.Date(2026, 11, 10, 15, 4, 0, 0, time.Local)
	scheduler.actions = []approve.Action{{At: at, What: "crm: POST /api/deals", Outcome: approve.Went}}
	rule := approve.Rule{Key: "push me/notes main", Label: "Push to me/notes: main"}
	scheduler.rules = []approve.Rule{rule}
	telegram.message(42, "/log")
	telegram.expect(t, "<b>Outside, last 24 hours</b>\n15:04 · crm: POST /api/deals · went\n\n<b>Always allowed</b>\n• Push to me/notes: main")
	telegram.expectButtons(t, "rf:"+rule.ID(), "x:")
	telegram.press("rf:" + rule.ID())
	telegram.expectAnswer(t, "den will ask about Push to me/notes: main again")
	telegram.expectEdit(t, "1: <b>Outside, last 24 hours</b>\n15:04 · crm: POST /api/deals · went\n\n<b>Always allowed</b>\nNothing yet. Press Always on a question to stop being asked about that kind of request.")

	telegram.press("zz:1")
	telegram.expectAnswer(t, "This button is out of date.")

	// /usage counts the tokens fx reported for the four turns.
	telegram.message(42, "/usage")
	telegram.expectPrefix(t, "Today\nchat: 400 in, 28 out, 4 turns\n\nLast 7 days: 400 in, 28 out")
}

func TestCompactWhenIdle(t *testing.T) {
	compactIdle, compactAbove = 200*time.Millisecond, 40_000
	t.Cleanup(func() { compactIdle, compactAbove = 50*time.Minute, 60_000 })
	cfg, telegram := startChat(t, nil)
	os.WriteFile(filepath.Join(cfg.Space, "context-size"), []byte("50000"), 0o644)
	catalogs := filepath.Join(filepath.Dir(filepath.Dir(cfg.Sessions)), "model-catalogs")
	os.MkdirAll(catalogs, 0o755)
	os.WriteFile(filepath.Join(catalogs, "p1.json"), []byte(`{"auto":{"limit":{"context":200000}}}`), 0o644)

	// After the user goes quiet, den compacts the session once, silently.
	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	compacted := filepath.Join(cfg.Space, "compacted")
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(compacted)
		if string(data) == "s1\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not compacted: %q", data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case text := <-telegram.sent:
		t.Fatalf("sent %q", text)
	case <-time.After(400 * time.Millisecond):
	}

	// The next reply says so, once.
	os.Remove(compacted)
	telegram.message(42, "back")
	telegram.expect(t, "🗜 While you were away, den compacted this session: 50k → 50k tokens.\n\nresume=s1 msg=back lines=1")
	if _, err := os.Stat(compacted); err == nil {
		t.Fatal("a user's turn compacted")
	}
}

func TestCompactCommand(t *testing.T) {
	cfg, telegram := startChat(t, nil)
	os.WriteFile(filepath.Join(cfg.Space, "context-size"), []byte("50000"), 0o644)
	catalogs := filepath.Join(filepath.Dir(filepath.Dir(cfg.Sessions)), "model-catalogs")
	os.MkdirAll(catalogs, 0o755)
	os.WriteFile(filepath.Join(catalogs, "p1.json"), []byte(`{"auto":{"limit":{"context":200000}}}`), 0o644)

	telegram.message(42, "/compact")
	telegram.expect(t, "There is no session to compact yet.")
	telegram.message(42, "/bug")
	telegram.expect(t, bugReport)

	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	telegram.message(42, "/compact")
	telegram.expect(t, "🗜 Compacted the session: 50k → 12k tokens.")
	if data, _ := os.ReadFile(filepath.Join(cfg.Space, "compacted")); string(data) != "s1\n" {
		t.Fatalf("compacted %q", data)
	}

	// A compaction the user asked for is not announced again, and the
	// agent goes on in the same session.
	telegram.message(42, "back")
	telegram.expect(t, "resume=s1 msg=back lines=1")
}

func TestSessionsList(t *testing.T) {
	den := &fakeDaemon{jobSessions: map[string]bool{"job": true}}
	_, telegram, c := startChatWith(t, den)

	now := time.Now()
	seed(t, c,
		store.Session{ID: "aaa", Kind: store.KindChat, Title: "Taxes", Updated: now.Add(-3 * time.Hour)},
		store.Session{ID: "bbb", Kind: store.KindChat, Title: "Trip", Updated: now.Add(-2 * time.Hour)},
		store.Session{ID: "job", Kind: store.KindJob, Title: "News alerts", Updated: now.Add(-time.Hour)},
	)

	// Job runs stay out, and the session switched to last comes first.
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions:")
	telegram.expectLabels(t, "Trip · 2h ago", "Taxes · 3h ago", "⏰ Job runs", "✕")
	telegram.expectButtons(t, "s:bbb", "s:aaa", "sj:", "x:")
	telegram.press("s:aaa")
	telegram.expectEdit(t, "1: ↺ Taxes · <b>auto</b> · p1")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t)
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions:")
	telegram.expectLabels(t, "• Taxes · just now", "Trip · 2h ago", "⏰ Job runs", "✕")
	telegram.expectButtons(t, "s:aaa", "s:bbb", "sj:", "x:")

	// Job runs have a list of their own, and the chats come back.
	telegram.press("sj:")
	telegram.expectEdit(t, "1: Recent job runs:")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "s:job", "sc:", "x:")
	telegram.press("sc:")
	telegram.expectEdit(t, "1: Recent sessions:")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "s:aaa", "s:bbb", "sj:", "x:")

	// Words find sessions of either kind by title.
	telegram.message(42, "/sessions ALERTS")
	telegram.expect(t, "Sessions with “ALERTS”:")
	telegram.expectLabels(t, "⏰ News alerts · 1h ago", "✕")
	telegram.expectButtons(t, "s:job", "x:")
	telegram.message(42, "/sessions boat")
	telegram.expect(t, "No session title has “boat”.")
}

func TestProgressNote(t *testing.T) {
	cfg, telegram := startChat(t, nil)
	c := New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	c.steering["s1"] = c.agent

	// A note from before the turn is not shown.
	c.Note("s1", "stale")
	lines, done := make(chan progress, 4), make(chan struct{})
	shown := make(chan struct{})
	go func() {
		c.showProgress(context.Background(), lines, done, true)
		close(shown)
	}()
	defer func() {
		close(done)
		<-shown
	}()

	lines <- progress{line: "Running den map -p @prompt.md jobs.jsonl"}
	time.Sleep(50 * time.Millisecond)
	// Another session's agent, such as a job's, or none, is not this turn.
	c.Note("", "🗂 from no session")
	c.Note("job1", "🗂 from a job")
	c.Note("s1", "🗂 3 of 10 items")
	telegram.expect(t, "Working…\n→ Running den map…\n   🗂 3 of 10 items")

	// The next step ends the note.
	lines <- progress{line: "Reading jobs.out.jsonl"}
	if got := edited(t, telegram); strings.Contains(got, "🗂") || !strings.Contains(got, "jobs.out.jsonl") {
		t.Fatalf("edited %q", got)
	}

	// What the agent said before a step goes under it, until the next.
	lines <- progress{line: "Fetching https://old.reddit.com/r/x"}
	lines <- progress{said: "Reddit gives a stub,\ntrying the old site."}
	if got := edited(t, telegram); !strings.HasSuffix(got, "→ Reading old.reddit.com…\n   Reddit gives a stub, trying the old site.") {
		t.Fatalf("edited %q", got)
	}
	lines <- progress{line: "Reading notes.md"}
	if got := edited(t, telegram); strings.Contains(got, "Reddit") {
		t.Fatalf("edited %q", got)
	}
}

// edited waits for the next edit of a message.
func edited(t *testing.T, telegram *fakeTelegram) string {
	t.Helper()
	select {
	case got := <-telegram.edited:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the progress message was not edited")
		return ""
	}
}

func TestTell(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	addSession(t, c, store.Session{ID: "main", Kind: store.KindChat})
	addSession(t, c, store.Session{ID: "job1", Kind: store.KindJob})

	if c.Tell("gone", "[den: done]", "done") || c.Tell("", "[den: done]", "done") {
		t.Fatal("a session den does not have took the report")
	}
	if !c.Tell("main", "[den: done]", "done") || !slices.Equal(inbox(t, c), []string{"[den: done]@main"}) {
		t.Fatalf("inbox %q", inbox(t, c))
	}

	// A report goes to its session however the chat's cursor moves.
	if err := c.enqueue(entry{text: "hi", session: "main"}); err != nil {
		t.Fatal(err)
	}
	c.switchTo("")
	if !c.Tell("main", "[den: again]", "again") || inbox(t, c)[2] != "[den: again]@main" {
		t.Fatalf("inbox %q", inbox(t, c))
	}

	// A job's session takes its reports too.
	if !c.Tell("job1", "[den: sent]", "sent") || inbox(t, c)[3] != "[den: sent]@job1" {
		t.Fatalf("inbox %q", inbox(t, c))
	}

	// /stop drops everything waiting and returns the reports' notices.
	stopped, notices := c.halt()
	if stopped || !slices.Equal(notices, []string{"done", "again", "sent"}) || len(inbox(t, c)) != 0 {
		t.Fatalf("stopped %v, notices %q, inbox %q", stopped, notices, inbox(t, c))
	}
}

func TestReplyTarget(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	ctx := context.Background()
	for _, s := range []store.Session{{ID: "main", Kind: store.KindChat}, {ID: "job1", Kind: store.KindJob, Title: "news"}} {
		if err := c.store.AddSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.enter(store.Telegram, "main"); err != nil {
		t.Fatal(err)
	}
	c.Sent([]int64{5, 6}, "job1")
	c.Sent([]int64{8}, "main")

	for message, want := range map[int64]string{6: "job1", 8: "main", 9: "main", 0: "main"} {
		if got, err := c.target(message); err != nil || got != want {
			t.Fatalf("target(%d) = %q, %v, want %q", message, got, err, want)
		}
	}
	if got := c.Label("job1"); got != "⏰ news" {
		t.Fatalf("label = %q", got)
	}
	if got := c.Label("main"); got != "" {
		t.Fatalf("the cursor's own session got the label %q", got)
	}

	// The ids last across restarts.
	c = New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	if got, _ := c.target(5); got != "job1" {
		t.Fatalf("after a restart target(5) = %q", got)
	}
}

func TestForget(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	ctx := context.Background()
	for _, id := range []string{"main", "old", "job1"} {
		if err := c.store.AddSession(ctx, store.Session{ID: id, Kind: store.KindChat}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.enter(store.Telegram, "main"); err != nil {
		t.Fatal(err)
	}
	c.Sent([]int64{5}, "old")
	c.Sent([]int64{6}, "job1")

	// A session the chat is not in goes, with its message links.
	if err := c.Forget("old", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.Session(ctx, "old"); err == nil {
		t.Fatal("old is still in the store")
	}
	if got, _ := c.target(5); got != "main" {
		t.Fatalf("a reply stayed tied to the deleted session: %q", got)
	}
	if got, _ := c.target(6); got != "job1" {
		t.Fatalf("a reply of another session went to %q", got)
	}

	// While a turn runs in the session, it stays.
	release := c.Occupy("main")
	if err := c.Forget("main", nil); err == nil {
		t.Fatal("forgot the session a turn runs in")
	}
	if c.cursor(store.Telegram) != "main" {
		t.Fatalf("cursor = %q", c.cursor(store.Telegram))
	}

	// Once it ends, the cursor goes with the session.
	release()
	if err := c.Forget("main", nil); err != nil {
		t.Fatal(err)
	}
	if got := c.cursor(store.Telegram); got != "" {
		t.Fatalf("cursor = %q after forgetting its session", got)
	}
}

func TestSwitchDuringTurn(t *testing.T) {
	_, telegram := startChat(t, nil)

	// A turn the chat left with /new still answers, headed with its
	// session, and the chat stays in the new one.
	telegram.message(42, "slow one")
	time.Sleep(300 * time.Millisecond)
	telegram.message(42, "/new")
	telegram.expect(t, "✦ New session · <b>auto</b> · p1")
	telegram.expect(t, "↩ Earlier session\n\nresume=none msg=slow one lines=1")
	telegram.message(42, "next")
	telegram.expect(t, "resume=none msg=next lines=1")
}

func TestBackground(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	names := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/p1/chat/completions" || body.Model != "auto" {
			http.Error(w, "wrong model", http.StatusBadRequest)
			return
		}
		title := "Modèles comparés."
		if strings.HasPrefix(body.Messages[1].Content, "slow: fail") {
			title = ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": title}}}})
	}))
	t.Cleanup(names.Close)
	original := mapper.Proxy
	mapper.Proxy = names.URL
	t.Cleanup(func() { mapper.Proxy = original })

	// A /bg task runs besides the chat: the chat answers first while it
	// still runs, and its answer comes headed with the title a model gave it.
	telegram.message(42, "/bg slow: compare two models\nwith care")
	telegram.expect(t, "⚙ Background task started. Its answer comes here; reply to it to go on there.")
	telegram.expectEdit(t, "7: ⚙ Modèles comparés runs in the background. Its answer comes here; reply to it to go on there.")
	telegram.message(42, "hi")
	telegram.expect(t, "resume=none msg=hi lines=1")
	telegram.expect(t, "⚙ Modèles comparés\n\nresume=none msg=slow: compare two models lines=2")
	// Its session is kept as a /bg task's, titled.
	list, err := c.store.Sessions(context.Background(), store.Filter{Kinds: []store.Kind{store.KindBackground}})
	if err != nil || len(list) != 1 || list[0].Title != "Modèles comparés" {
		t.Fatalf("background sessions %+v, %v", list, err)
	}

	// Without a title from the model, the heading says only the kind.
	telegram.message(42, "/bg slow: fail me")
	telegram.expect(t, "⚙ Background task started. Its answer comes here; reply to it to go on there.")
	telegram.expect(t, "⚙ Background task\n\nresume=none msg=slow: fail me lines=1")

	telegram.message(42, "/bg")
	telegram.expect(t, "Send /bg with a task, such as: /bg compare these two models")
}

func TestBackgroundReports(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, usage.Open(cfg.State), testStore(t, cfg))
	ctx := context.Background()
	if err := c.store.AddSession(ctx, store.Session{ID: "task", Kind: store.KindBackground}); err != nil {
		t.Fatal(err)
	}
	release := c.Occupy("task")

	// A report goes to the task's session, where it waits while the task
	// runs.
	if !c.Tell("task", "[den: done]", "done") || !slices.Equal(inbox(t, c), []string{"[den: done]@task"}) {
		t.Fatalf("inbox %q", inbox(t, c))
	}
	if next, _ := c.store.NextTurn(ctx, c.occupied()...); next != "" {
		t.Fatalf("a turn would run in the busy session %q", next)
	}
	if err := c.Forget("task", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("Forget while the task runs: %v", err)
	}
	release()
	if next, _ := c.store.NextTurn(ctx, c.occupied()...); next != "task" {
		t.Fatalf("next turn in %q once the task ended", next)
	}
}

func TestBgTask(t *testing.T) {
	if task, ok := bgTask("/bg@den_bot do it\nnow"); !ok || task != "do it\nnow" {
		t.Errorf("bgTask = %q, %v", task, ok)
	}
	if _, ok := bgTask("/bgx do it"); ok {
		t.Error("/bgx is not /bg")
	}
}

func TestSilentReport(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	// The agent's commands name the session it holds.
	session := c.cursor(store.Telegram)
	if data, _ := os.ReadFile(filepath.Join(c.cfg.Space, "den-session")); string(data) != session {
		t.Fatalf("DEN_SESSION %q, want %q", data, session)
	}

	// A turn den's report started may end without a word to the user.
	if !c.Tell(session, "[den: quiet run ended]", "") {
		t.Fatal("the chat's turn did not take the report")
	}
	time.Sleep(300 * time.Millisecond)
	c.Quiet(session)
	select {
	case text := <-telegram.sent:
		t.Fatalf("sent %q", text)
	case <-time.After(2 * time.Second):
	}

	telegram.message(42, "and you?")
	telegram.expect(t, "resume=s1 msg=and you? lines=1")
}

func TestQuieted(t *testing.T) {
	c := &Chat{quiet: map[string]time.Time{}}
	started := time.Now()

	if c.Quieted("s1", started) {
		t.Error("a turn without a mark was quiet")
	}

	c.Quiet("s1")
	if !c.Quieted("s1", started) {
		t.Error("a mark after the turn's start did not silence it")
	}
	if c.Quieted("s1", started) {
		t.Error("the mark was not consumed")
	}

	// A mark left by an earlier turn does not silence the next one, and
	// that turn consumes it.
	c.Quiet("s1")
	if c.Quieted("s1", time.Now().Add(time.Second)) {
		t.Error("a mark from before the turn's start silenced it")
	}
	if c.Quieted("s1", started) {
		t.Error("the stale mark was not consumed")
	}

	c.Quiet("s1")
	if c.Quieted("s2", started) {
		t.Error("a mark for another session silenced the turn")
	}
}

func TestQuietTurn(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	session := c.cursor(store.Telegram)

	// The agent runs den quiet while its turn goes on, so its reply stays
	// in the session.
	telegram.message(42, "slow one")
	time.Sleep(300 * time.Millisecond)
	c.Quiet(session)
	select {
	case text := <-telegram.sent:
		t.Fatalf("sent %q", text)
	case <-time.After(2 * time.Second):
	}

	// The next turn is not silenced by the mark.
	telegram.message(42, "and you?")
	telegram.expect(t, "resume=s1 msg=and you? lines=1")
}

func TestGrid(t *testing.T) {
	for n, want := range map[int]string{1: "1", 3: "3", 4: "2 2", 5: "3 2", 6: "3 3", 7: "3 2 2"} {
		buttons := make([]notify.Button, n)
		var got []string
		for _, row := range grid(buttons, 3) {
			got = append(got, strconv.Itoa(len(row)))
		}
		if s := strings.Join(got, " "); s != want {
			t.Errorf("%d buttons: rows %s, want %s", n, s, want)
		}
	}
}

func TestAnswerFailure(t *testing.T) {
	got := answer(runner.Reply{Error: "fx exited: exit status 1", Stderr: "[notice] x\nboom"}, true)
	if want := "fx failed: fx exited: exit status 1\nboom\nSend /new if this session cannot continue."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got = answer(runner.Reply{Error: "SubscriptionNativeImageUnavailable", Stderr: "[notice] x"}, true)
	if got != fxErrors["SubscriptionNativeImageUnavailable"] {
		t.Errorf("an image the model cannot see: got %q", got)
	}
}

func TestInboxSurvivesARestart(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, nil, testStore(t, cfg))
	addSession(t, c, store.Session{ID: "s1", Kind: store.KindChat})
	if err := c.enqueue(
		entry{text: "Call the bank", image: "/inbox/a.jpg", session: "s1"},
		entry{text: "[den: Mail to a@example.com was not sent.]", report: true, session: "s1"},
	); err != nil {
		t.Fatal(err)
	}

	again := New(cfg, nil, nil, testStore(t, cfg))
	list, err := again.store.Inbox(context.Background())
	if err != nil || len(list) != 2 || list[0].Text != "Call the bank" || list[0].Image != "/inbox/a.jpg" {
		t.Fatalf("inbox = %+v, %v", list, err)
	}
	if m := list[1]; !m.Report || m.Session != "s1" {
		t.Errorf("the report came back as %+v", m)
	}
	select {
	case <-again.wake:
	default:
		t.Error("the worker is not woken for what waited")
	}
}

func TestAppTurn(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	list, stop := c.Events.Listen()
	defer stop()

	next := func(kind string) events.Event {
		t.Helper()
		for {
			select {
			case e := <-list:
				if e.Type == kind {
					return e
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no %s event", kind)
			}
		}
	}

	// A message from the app answers only there.
	c.Post(context.Background(), "hello", "", nil)
	if e := next(events.Message); e.Text != "hello" {
		t.Fatalf("message %+v", e)
	}
	first := next(events.Reply)
	if first.Text != "resume=none msg=hello lines=1" || first.Session == "" {
		t.Fatalf("reply %+v", first)
	}
	next(events.Idle)
	select {
	case got := <-telegram.sent:
		t.Fatalf("Telegram got %q", got)
	case <-time.After(300 * time.Millisecond):
	}

	// One from Telegram answers in both.
	telegram.message(42, "and you")
	if e := next(events.Message); e.Text != "and you" {
		t.Fatalf("message %+v", e)
	}
	// Telegram has a cursor of its own, so this starts its own session.
	want := "resume=none msg=and you lines=1"
	telegram.expect(t, want)
	if e := next(events.Reply); e.Text != want {
		t.Fatalf("reply %+v", e)
	}
	next(events.Idle)

	// den's report in the app's session answers only in the app too, since
	// Telegram's chat never went there.
	if !c.Tell(first.Session, "[den: done]", "done") {
		t.Fatal("the report was not kept")
	}
	if e := next(events.Reply); e.Session != first.Session || !strings.Contains(e.Text, "msg=[den: done]") {
		t.Fatalf("reply %+v", e)
	}
	next(events.Idle)
	select {
	case got := <-telegram.sent:
		t.Fatalf("Telegram got %q", got)
	case <-time.After(300 * time.Millisecond):
	}

	// The app's message in Telegram's session answers in both.
	own := c.cursor(store.Telegram)
	c.Post(context.Background(), "also", own, nil)
	want = "resume=" + own + " msg=also lines=1"
	telegram.expect(t, want)
	if e := next(events.Reply); e.Session != own || e.Text != want {
		t.Fatalf("reply %+v", e)
	}
	next(events.Idle)

	// A reply to a job's run goes on in its session, on the job's model, and
	// only in the app.
	seed(t, c, store.Session{ID: "job1", Kind: store.KindJob, Title: "news", Choice: store.Choice{Provider: "p2", Model: "m"}})
	c.Post(context.Background(), "more", "job1", nil)
	if e := next(events.Message); e.Session != "job1" {
		t.Fatalf("message %+v", e)
	}
	if e := next(events.Reply); !strings.HasPrefix(e.Text, "resume=job1 msg=more") || !strings.Contains(e.Text, "provider=p2 model=m") {
		t.Fatalf("reply %+v", e)
	}
	select {
	case got := <-telegram.sent:
		t.Fatalf("Telegram got %q", got)
	case <-time.After(300 * time.Millisecond):
	}
	next(events.Idle)

	// A new task from the app starts a session of its own, beside the
	// app's, and leaves the app where it was.
	cursor := c.cursor(appClient)
	started, err := c.Start(context.Background(), "plan a trip", nil)
	if err != nil {
		t.Fatal(err)
	}
	if e := next(events.Message); e.Session != started || started == cursor || e.Text != "plan a trip" {
		t.Fatalf("message %+v, cursor %s", e, cursor)
	}
	if e := next(events.Reply); e.Session != started || !strings.HasPrefix(e.Text, "resume=none msg=plan a trip") {
		t.Fatalf("reply %+v", e)
	}
	next(events.Idle)
	if c.cursor(appClient) != cursor {
		t.Fatalf("cursor moved to %s", c.cursor(appClient))
	}

	// Files from the app are saved, and a picture among them is attached.
	png := "\x89PNG\r\n\x1a\n picture"
	files := []File{{Name: "shot.png", Data: []byte(png)}, {Name: "notes.txt", Data: []byte("notes")}}
	if err := c.Post(context.Background(), "look", "", files); err != nil {
		t.Fatal(err)
	}
	// The app gets one message with both files, kept in the session for it.
	e := next(events.Message)
	if e.Text != "look" || len(e.Files) != 2 || e.Files[0].Name != "shot.png" || e.Files[1].Name != "notes.txt" || e.Files[1].Size != 5 {
		t.Fatalf("message %+v", e)
	}
	expectFile(t, c.Events.FilePath(e.Session, e.Files[0].File), png)
	if e := next(events.Reply); !strings.Contains(e.Text, "msg=look") || !strings.HasSuffix(e.Text, "images=1") {
		t.Fatalf("reply %+v", e)
	}
	saved, _ := filepath.Glob(filepath.Join(c.cfg.Inbox, "*", "*"))
	if len(saved) != 2 || !strings.HasSuffix(saved[0], "-0-shot.png") || !strings.HasSuffix(saved[1], "-1-notes.txt") {
		t.Fatalf("saved %v", saved)
	}
	expectFile(t, saved[0], png)
}

// TestNewSessions keeps each new session's messages and replies in it, by
// the id den gave it, also when the chat moves on before its turn.
func TestNewSessions(t *testing.T) {
	_, _, c := startChatWith(t, nil)
	list, stop := c.Events.Listen()
	defer stop()
	ctx := context.Background()

	// s1 is the app's session; while its turn runs, the app leaves it,
	// writes in a new one, s2, and leaves that before s2's turn.
	if err := c.Post(ctx, "slow first", "", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.Open(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if setup, err := c.Setup(ctx); err != nil || setup.Session != "" {
		t.Fatalf("setup %+v: %v", setup, err)
	}
	if err := c.Post(ctx, "test", "", nil); err != nil {
		t.Fatal(err)
	}
	if setup, _ := c.Setup(ctx); setup.Session != "s2" {
		t.Fatalf("the app is in %q", setup.Session)
	}
	if err := c.Open(ctx, ""); err != nil {
		t.Fatal(err)
	}

	got := map[string][]string{}
	idle := map[string]bool{}
	timeout := time.After(10 * time.Second)
	for !idle["s1"] || !idle["s2"] {
		select {
		case e := <-list:
			switch e.Type {
			case events.Message, events.Reply:
				got[e.Session] = append(got[e.Session], e.Text)
			case events.Idle:
				idle[e.Session] = true
			}
		case <-timeout:
			t.Fatalf("events %q, idle %v", got, idle)
		}
	}
	want := map[string][]string{
		"s1": {"slow first", "resume=none msg=slow first lines=1"},
		"s2": {"test", "resume=none msg=test lines=1"},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("events %q", got)
	}
	for session, texts := range want {
		journal, err := events.Journal(c.Events.Dir, session)
		if err != nil || len(journal) != 2 || journal[0].Text != texts[0] || journal[1].Text != texts[1] {
			t.Fatalf("%s journal %+v: %v", session, journal, err)
		}
	}
	if setup, _ := c.Setup(ctx); setup.Session != "" {
		t.Fatalf("the app is still in %q", setup.Session)
	}
}

func TestAppBackground(t *testing.T) {
	_, telegram, c := startChatWith(t, nil)
	names := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "compare-models"}}}})
	}))
	t.Cleanup(names.Close)
	original := mapper.Proxy
	mapper.Proxy = names.URL
	t.Cleanup(func() { mapper.Proxy = original })
	list, stop := c.Events.Listen()
	defer stop()

	// A /bg task from the app runs in a session of its own and answers only
	// in the app, under its name.
	if err := c.Post(context.Background(), "/bg slow: compare models", "", nil); err != nil {
		t.Fatal(err)
	}
	var message, reply *events.Event
	timeout := time.After(5 * time.Second)
	for message == nil || reply == nil {
		select {
		case e := <-list:
			switch e.Type {
			case events.Message:
				message = &e
			case events.Reply:
				reply = &e
			}
		case <-timeout:
			t.Fatalf("message %+v, reply %+v", message, reply)
		}
	}
	if message.Text != "slow: compare models" || message.Title != "compare-models" || message.Session == "" {
		t.Fatalf("message %+v", message)
	}
	if reply.Text != "resume=none msg=slow: compare models lines=1" || reply.Title != message.Title || reply.Session != message.Session {
		t.Fatalf("reply %+v", reply)
	}
	select {
	case got := <-telegram.sent:
		t.Fatalf("Telegram got %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestSend keeps the files a turn sends for its reply, and sends a file
// that comes after its turn to its session.
func TestSend(t *testing.T) {
	cfg, telegram, c := startChatWith(t, nil)
	c.Events.Dir = t.TempDir()
	list, stop := c.Events.Listen()
	defer stop()
	next := func(kind string) events.Event {
		t.Helper()
		for {
			select {
			case e := <-list:
				if e.Type == kind {
					return e
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no %s event", kind)
			}
		}
	}
	running := func() string {
		t.Helper()
		waitFor(t, func() bool {
			data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
			return string(data) == "slow" && c.agentBusy()
		})
		c.mu.Lock()
		defer c.mu.Unlock()
		for session, agent := range c.steering {
			if agent == c.agent {
				return session
			}
		}
		return ""
	}
	upload := func(name string) Upload {
		return Upload{Upload: notify.Upload{Name: name, Data: []byte(name)}}
	}
	quiet := func() {
		t.Helper()
		select {
		case got := <-telegram.sent:
			t.Fatalf("Telegram got %q", got)
		case <-time.After(300 * time.Millisecond):
		}
	}

	// A turn of Telegram's sends its files after its reply, the pictures
	// as one album, and the app has them in the reply.
	telegram.message(42, "slow")
	session := running()
	for _, name := range []string{"a.png", "notes.pdf", "b.jpg"} {
		if err := c.Send(context.Background(), session, upload(name)); err != nil {
			t.Fatal(err)
		}
	}
	telegram.expect(t, "resume=none msg=slow lines=1")
	telegram.expect(t, "📎 sendMediaGroup a.png b.jpg")
	telegram.expect(t, "📎 sendDocument notes.pdf")
	reply := next(events.Reply)
	if len(reply.Files) != 3 || reply.Files[0].Name != "a.png" || reply.Files[1].Size != len("notes.pdf") {
		t.Fatalf("reply files %+v", reply.Files)
	}
	if data, err := os.ReadFile(c.Events.FilePath(reply.Session, reply.Files[0].File)); err != nil || string(data) != "a.png" {
		t.Fatalf("kept %q, %v", data, err)
	}
	next(events.Idle)

	// A turn of the app's sends them only there.
	c.Post(context.Background(), "slow", "", nil)
	session = running()
	if err := c.Send(context.Background(), session, upload("c.png")); err != nil {
		t.Fatal(err)
	}
	if e := next(events.Reply); len(e.Files) != 1 || e.Files[0].Name != "c.png" {
		t.Fatalf("app reply %+v", e)
	}
	next(events.Idle)
	quiet()

	// So does a turn on an agent of its own, such as a new task's.
	started, err := c.Start(context.Background(), "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
		c.mu.Lock()
		defer c.mu.Unlock()
		_, runs := c.attached[started]
		return string(data) == "slow" && runs
	})
	if err := c.Send(context.Background(), started, upload("d.png")); err != nil {
		t.Fatal(err)
	}
	if e := next(events.Reply); e.Session != started || len(e.Files) != 1 || e.Files[0].Name != "d.png" {
		t.Fatalf("new task's reply %+v", e)
	}
	next(events.Idle)
	quiet()

	// A file that comes once the turn is over goes on its own, to the
	// session and Telegram.
	if err := c.Send(context.Background(), session, upload("late.png")); err != nil {
		t.Fatal(err)
	}
	if e := next(events.File); len(e.Files) != 1 || e.Files[0].Name != "late.png" || e.Session != session {
		t.Fatalf("late file %+v", e)
	}
	telegram.expect(t, "📎 sendPhoto late.png")

	// One from a session the chat's agent does not run goes to Telegram.
	if err := c.Send(context.Background(), "job", upload("job.pdf")); err != nil {
		t.Fatal(err)
	}
	telegram.expect(t, "📎 sendDocument job.pdf")
}

// testStore opens a store in cfg's state, which a test that makes a second
// chat on the same state shares.
func testStore(t *testing.T, cfg config.Config) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seed adds sessions to the chat's store, and to fx's index, where fx keeps
// the sessions it can resume.
func seed(t *testing.T, c *Chat, sessions ...store.Session) {
	t.Helper()
	if err := os.MkdirAll(c.cfg.Sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(c.cfg.Sessions, "index.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, s := range sessions {
		if err := c.store.AddSession(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(f, `{"op":"put","id":%q,"role":"root","title":%q,"updated":%d}`+"\n", s.ID, s.Title, s.Updated.UnixMilli())
	}
}

// TestRunning checks that a client reading the setup sees each running turn
// with its latest step, and no longer once it ends.
func TestRunning(t *testing.T) {
	ctx := context.Background()
	_, telegram, c := startChatWith(t, nil)
	telegram.message(42, "hello")
	telegram.expect(t, "resume=none msg=hello lines=1")
	session := c.cursor(store.Telegram)

	telegram.message(42, "steps")
	waitFor(t, func() bool {
		setup, _ := c.Setup(ctx)
		return setup.Running[session] == "Running ls"
	})

	waitFor(t, func() bool {
		setup, _ := c.Setup(ctx)
		_, running := setup.Running[session]
		return !running
	})
}

// TestDrain lets the running turn end and starts none for what comes
// meanwhile, which waits for den's next start.
func TestDrain(t *testing.T) {
	cfg, telegram, c := startChatWith(t, nil)
	telegram.message(42, "slow")
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
		return string(data) == "slow"
	})

	c.Drain()
	if c.Idle() {
		t.Fatal("idle while a turn runs")
	}
	telegram.expect(t, "resume=none msg=slow lines=1")
	waitFor(t, c.Idle)

	telegram.message(42, "later")
	select {
	case got := <-telegram.sent:
		t.Fatalf("a turn started while den stops: %q", got)
	case <-time.After(500 * time.Millisecond):
	}
	if !c.waiting() {
		t.Error("the message did not wait in the inbox")
	}
}

// TestStopCutsTurn tells the user of a turn den stopped before it ended.
func TestStopCutsTurn(t *testing.T) {
	cfg, telegram, c := startChatWith(t, nil)
	telegram.message(42, "slow")
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
		return string(data) == "slow"
	})

	stopChat(c)
	telegram.expect(t, "den restarted before this turn ended. Write to go on.")
}

func TestArchiveIdle(t *testing.T) {
	_, _, c := startChatWith(t, nil)
	ctx := context.Background()
	old := time.Now().Add(-ArchiveAfter - time.Hour)
	for _, id := range []string{"idle", "running", "asking"} {
		if err := c.store.AddSession(ctx, store.Session{ID: id, Kind: store.KindChat, Created: old}); err != nil {
			t.Fatal(err)
		}
	}
	events, stop := c.Events.Listen()
	defer stop()

	// A session whose turn runs, or whose question waits, stays.
	c.mu.Lock()
	c.busy["running"] = 1
	c.mu.Unlock()
	if err := c.ArchiveIdle(ctx, []string{"asking"}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	delete(c.busy, "running")
	c.mu.Unlock()

	select {
	case e := <-events:
		if e.Type != "archived" || e.Session != "idle" || !e.Archived {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	list, _ := c.store.Sessions(ctx, store.Filter{Shelf: store.Archive})
	if len(list) != 1 || list[0].ID != "idle" {
		t.Fatalf("archived %+v", list)
	}
}
