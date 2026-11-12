package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/usage"
)

// fakeFX answers with the session it resumed and the first line of the
// prompt, which holds the user's words.
const fakeFX = `#!/bin/sh
case "$1" in
status) printf '{"model_source":"%s","model":"auto","connected_providers":["p1","p2"]}\n' "${FX_PROVIDER:-p1}"; exit ;;
models) printf '{"ids":["auto","fast-%s","big-%s"]}\n' "${FX_PROVIDER:-p1}" "${FX_PROVIDER:-p1}"; exit ;;
esac
resume=none
image=
while [ $# -gt 0 ]; do
	case "$1" in
	--resume) resume=$2; shift ;;
	--image) image=" image=$2"; shift ;;
	--provider) image="$image provider=$2"; shift ;;
	--model) image="$image model=$2"; shift ;;
	--effort) image="$image effort=$2"; shift ;;
	--) shift; break ;;
	esac
	shift
done
printf '%s' "$*" > last-prompt
if [ -n "$FX_AUTO_COMPACT_PERCENT" ]; then echo "$FX_AUTO_COMPACT_PERCENT $resume" > compacted; fi
first=$(printf '%s\n' "$*" | head -n 1)
lines=$(printf '%s\n' "$*" | grep -c .)
case "$first" in
slow*) sleep 1 ;;
steps*) echo "Reading a.txt" >&2; echo "total 0" >&2; echo "Running ls" >&2; echo "Running ls -la" >&2; sleep 1 ;;
quota*) case "$image" in *effort=low*) ;; *) echo "fx ask: API request failed · HTTP 402 · out of credits" >&2; exit 1 ;; esac ;;
esac
printf '{"final_output":"resume=%s msg=%s lines=%s%s","session_id":"s1","usage":{"input_tokens":100,"output_tokens":7}}\n' "$resume" "$first" "$lines" "$image"
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
		var buttons []string
		if markup, ok := body["reply_markup"].(map[string]any); ok {
			for _, row := range markup["inline_keyboard"].([]any) {
				for _, b := range row.([]any) {
					buttons = append(buttons, b.(map[string]any)["callback_data"].(string))
				}
			}
		}
		f.mu.Lock()
		f.buttons = buttons
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
	go func() {
		New(cfg, den, usage.Open(cfg.State)).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cfg, telegram
}

func TestChat(t *testing.T) {
	cfg, telegram := startChat(t, nil)

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
	telegram.message(42, "fresh")
	telegram.expect(t, "resume=none msg=fresh lines=1")

	telegram.message(42, "/stop")
	telegram.expect(t, "Nothing is running.")

	// A turn shows the steps fx reports, then deletes them before replying.
	telegram.message(42, "steps")
	telegram.expect(t, "Working…\n• Read a.txt\n→ Running ls ×2…")
	select {
	case id := <-telegram.deleted:
		if id != 7 {
			t.Fatalf("deleted message %d, want 7", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the progress message was not deleted")
	}
	telegram.expect(t, "resume=s1 msg=steps lines=1")

	// A photo is saved, named in the turn, and attached.
	day := filepath.Join(cfg.Inbox, time.Now().Format("2006-01-02"))
	id := telegram.update(42, map[string]any{
		"caption": "what is this",
		"photo":   []any{map[string]any{"file_id": "small"}, map[string]any{"file_id": "large"}},
	})
	photo := filepath.Join(day, fmt.Sprintf("%d-photo.jpg", id))
	telegram.expect(t, "resume=s1 msg=what is this lines=2 image="+photo)
	expectFile(t, photo, "content of large")

	// A document keeps a safe form of its name and is not attached.
	id = telegram.update(42, map[string]any{
		"document": map[string]any{"file_id": "doc", "file_name": "../.Report v1.pdf"},
	})
	document := filepath.Join(day, fmt.Sprintf("%d-Report_v1.pdf", id))
	telegram.expect(t, "resume=s1 msg=[The user sent a file, saved at "+document+"] lines=1")
	expectFile(t, document, "content of doc")

	telegram.update(42, map[string]any{"sticker": map[string]any{"file_id": "s"}})
	telegram.expect(t, "den reads text, photos, voice messages, and files for now.")

	// A voice message comes with its transcript.
	id = telegram.update(42, map[string]any{"voice": map[string]any{"file_id": "talk"}})
	telegram.expect(t, "resume=s1 msg=heard content of talk lines=2")
	prompt, _ = os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
	if !strings.HasPrefix(string(prompt), "heard content of talk\n[The user said this in a voice message") {
		t.Fatalf("voice prompt %q", prompt)
	}
	expectFile(t, filepath.Join(day, fmt.Sprintf("%d-voice.ogg", id)), "content of talk")

	// Messages sent during a turn go together as the next one.
	// They are marked with 👀; a message that starts a turn is not.
	slow := telegram.update(42, map[string]any{"text": "slow one"})
	time.Sleep(300 * time.Millisecond)
	a := telegram.update(42, map[string]any{"text": "a"})
	b := telegram.update(42, map[string]any{"text": "b"})
	telegram.expect(t, "resume=s1 msg=slow one lines=1")
	telegram.expect(t, "resume=s1 msg=a lines=2")
	telegram.mu.Lock()
	reacted := strings.Join(telegram.reacted, ", ")
	telegram.mu.Unlock()
	if want := fmt.Sprintf("%d 👀, %d 👀", a, b); reacted != want {
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

	data, err := os.ReadFile(filepath.Join(cfg.State, "chat.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"offset":17`) {
		t.Fatalf("state %s does not record offset 17", data)
	}
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
	ran  []string
	// questions maps a waiting question's id to its text, and answers
	// records each answer as id=true or id=false.
	questions map[string]string
	answers   []string
	// rules and actions are what /log shows.
	rules   []approve.Rule
	actions []approve.Action
	// context is the size every turn reports.
	context int
	// jobSessions are the sessions of job runs.
	jobSessions map[string]bool
}

func (f *fakeDaemon) ContextSize(since time.Time) int { return f.context }

func (f *fakeDaemon) LastContext(since time.Time) int { return f.context / 4 }

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

func (f *fakeDaemon) StartJob(ref string) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.list {
		if j.ID == ref {
			f.ran = append(f.ran, ref)
			return j, nil
		}
	}
	return jobs.Job{}, fmt.Errorf("no job %s", ref)
}

func (f *fakeDaemon) SetJobSlot(ref, slot string) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, j := range f.list {
		if j.ID == ref {
			f.list[i].Slot = strings.TrimPrefix(slot, jobs.Main)
			return f.list[i], nil
		}
	}
	return jobs.Job{}, fmt.Errorf("no job %s", ref)
}

func (f *fakeDaemon) RemoveJob(ref string) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, j := range f.list {
		if j.ID == ref {
			f.list = append(f.list[:i], f.list[i+1:]...)
			return j, nil
		}
	}
	return jobs.Job{}, fmt.Errorf("no job %s", ref)
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
	scheduler := &fakeDaemon{questions: map[string]string{"1": "Send it?", "2": "Push it?"}, list: []jobs.Job{{ID: "j1", Name: "reflect", Schedule: "0 4 * * *", Prompt: "Use the reflect skill.", NextRun: next}}}
	cfg, telegram := startChat(t, scheduler)

	now := time.Now()
	index := fmt.Sprintf(`{"op":"put","id":"aaa","role":"root","title":"Taxes","updated":%d}
{"op":"put","id":"bbb","role":"root","title":"Trip","updated":%d}
{"op":"put","id":"ccc","role":"child","title":"A subagent","updated":%d}
`, now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	if err := os.MkdirAll(cfg.Sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Sessions, "index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	// /sessions lists root sessions, newest first, and a button switches.
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions. Pick one to continue it:\n\n1. Trip, 1h ago\n2. Taxes, 2h ago")
	telegram.expectButtons(t, "s:bbb", "s:aaa", "sj:", "x:")
	// ✕ deletes the list.
	telegram.press("x:")
	telegram.expectAnswer(t, "")
	if id := <-telegram.deleted; id != 1 {
		t.Fatalf("deleted %d", id)
	}
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions. Pick one to continue it:\n\n1. Trip, 1h ago\n2. Taxes, 2h ago")
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
	telegram.expect(t, "resume=s1 msg=redo this lines=2")
	prompt, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
	if want := "redo this\n[In reply to your earlier message: «Here is the plan»]"; string(prompt) != want {
		t.Fatalf("prompt %q, want %q", prompt, want)
	}

	// /jobs offers to run and, after a confirmation, remove each job.
	telegram.message(42, "/jobs")
	telegram.expectPrefix(t, "Jobs:\n\n<b>reflect</b>: 0 4 * * *, next Tue 10 Nov 04:00, never ran\nUse the reflect skill.")
	telegram.expectButtons(t, "jr:j1", "jd:j1", "x:")
	telegram.press("jr:j1")
	telegram.expectAnswer(t, "Started")
	telegram.expect(t, "Started job reflect. Its reply comes when it finishes.")
	telegram.press("jd:j1")
	telegram.expectAnswer(t, "")
	telegram.expect(t, "Remove job reflect?\nUse the reflect skill.")
	telegram.expectButtons(t, "jx:j1", "k:")
	telegram.press("jx:j1")
	telegram.expectAnswer(t, "Removed")
	telegram.expect(t, "Removed job reflect.")
	telegram.message(42, "/jobs")
	telegram.expect(t, "No jobs. Ask the agent to schedule one.")
	if strings.Join(scheduler.ran, ",") != "j1" {
		t.Fatalf("ran %v", scheduler.ran)
	}

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
	telegram.expect(t, "⚙︎ <b>big-p2</b> · p2\n\nHow hard it thinks: more is slower and costs more.")
	telegram.expectButtons(t, "e:default", "e:low", "e:high", "x:")
	telegram.press("e:high")
	telegram.expectEdit(t, "1: ⚙︎ <b>big-p2</b> · p2 · effort high")
	telegram.expectAnswer(t, "")
	telegram.message(42, "/effort max")
	telegram.expect(t, "big-p2 takes the effort low, high, or default.")

	// The second model is chosen from the recent ones, with its own effort.
	telegram.message(42, "/model")
	telegram.expectPrefix(t, "⚙︎ <b>big-p2</b> · p2\nSecond: none")
	telegram.press("2:")
	telegram.expectEdit(t, "1: ⚙︎ Second: none\n\nJobs set to the second model run on it, so a change here reaches them all.")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "n:p2/big-p2", "n:p1/big-p1", "n:", "r:", "A:p2:0", "x:")
	telegram.press("n:p2/big-p2")
	telegram.expectEdit(t, "1: ⚙︎ Second: <b>big-p2</b> · p2")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "f:default", "f:low", "f:high")
	telegram.press("f:low")
	telegram.expectEdit(t, "1: ⚙︎ Second: <b>big-p2</b> · p2 · effort low")
	telegram.expectAnswer(t, "")
	telegram.message(42, "/model")
	telegram.expectPrefix(t, "⚙︎ <b>big-p2</b> · p2\nSecond: <b>big-p2</b> · p2 · effort low")
	telegram.message(42, "hey")
	telegram.expect(t, "resume=s1 msg=hey lines=1 provider=p2 model=big-p2 effort=high")

	// /again asks the second model in the same session, once.
	telegram.message(42, "/again")
	telegram.expect(t, "↻ big-p2\n\nresume=s1 msg=[The user asked you, a different model, to answer their last message again. The answer above came from another model: check it, correct it, and give your own best answer.] lines=1 provider=p2 model=big-p2 effort=low")
	telegram.message(42, "/again what about tea")
	telegram.expect(t, "↻ big-p2\n\nresume=s1 msg=what about tea lines=1 provider=p2 model=big-p2 effort=low")
	telegram.message(42, "and coffee")
	telegram.expect(t, "resume=s1 msg=and coffee lines=1 provider=p2 model=big-p2 effort=high")

	// When the main model's provider fails, the second one answers.
	telegram.message(42, "quota is gone")
	telegram.expect(t, "↻ big-p2 · big-p2 failed: fx ask: API request failed · HTTP 402 · out of credits\n\nresume=s1 msg=quota is gone lines=1 provider=p2 model=big-p2 effort=low")
	telegram.message(42, "/new")
	telegram.expect(t, "✦ New session · <b>big-p2</b> · p2 · effort high")
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

	// A button answers a question den asked, once.
	telegram.press("no:1")
	telegram.expectEdit(t, "1: Send it?\n✗ Denied")
	telegram.expectAnswer(t, "")
	telegram.press("ok:1")
	telegram.expectAnswer(t, "This question has expired.")
	telegram.press("al:2")
	telegram.expectEdit(t, "1: Push it?\n✓ Always allowed. /log lists what you always allow.")
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
	telegram.expectPrefix(t, "Today\nchat: 800 in, 56 out, 8 turns\n\nLast 7 days: 800 in, 56 out")
}

func TestCompactWhenIdle(t *testing.T) {
	compactIdle, compactAbove = 200*time.Millisecond, 40_000
	t.Cleanup(func() { compactIdle, compactAbove = 50*time.Minute, 60_000 })
	cfg, telegram := startChat(t, &fakeDaemon{context: 50_000})
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
		if string(data) == "10 s1\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not compacted: %q", data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	prompt, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
	if !strings.HasPrefix(string(prompt), "[den: the user has been away") {
		t.Fatalf("compacting prompt %q", prompt)
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
	den := &fakeDaemon{context: 50_000}
	cfg, telegram := startChat(t, den)
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
	got := telegram.expectPrefix(t, "🗜 Compacted the session: 50k → ")
	if data, _ := os.ReadFile(filepath.Join(cfg.Space, "compacted")); string(data) != "10 s1\n" {
		t.Fatalf("compacted %q, reply %q", data, got)
	}

	// A compaction the user asked for is not announced again.
	telegram.message(42, "back")
	telegram.expect(t, "resume=s1 msg=back lines=1")

	// fx does not compact a session under compactPercent of the window.
	den.mu.Lock()
	den.context = 10_000
	den.mu.Unlock()
	telegram.message(42, "small")
	telegram.expect(t, "resume=s1 msg=small lines=1")
	telegram.message(42, "/compact")
	telegram.expect(t, "Context is 10k of 200k: fx compacts a session from 10% of the window, so this one stays as it is.")
}

func TestSessionsList(t *testing.T) {
	den := &fakeDaemon{jobSessions: map[string]bool{"job": true}}
	cfg, telegram := startChat(t, den)

	now := time.Now()
	index := fmt.Sprintf(`{"op":"put","id":"aaa","role":"root","title":"Taxes","updated":%d}
{"op":"put","id":"bbb","role":"root","title":"Trip","updated":%d}
{"op":"put","id":"job","role":"root","title":"News alerts","updated":%d}
{"op":"put","id":"gone","role":"root","title":"Gone","updated":%d}
{"op":"del","id":"gone","ts":1}
`, now.Add(-3*time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	os.MkdirAll(cfg.Sessions, 0o755)
	os.WriteFile(filepath.Join(cfg.Sessions, "index.jsonl"), []byte(index), 0o644)

	// Job runs stay out, and the session switched to last comes first.
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions. Pick one to continue it:\n\n1. Trip, 2h ago\n2. Taxes, 3h ago")
	telegram.expectButtons(t, "s:bbb", "s:aaa", "sj:", "x:")
	telegram.press("s:aaa")
	telegram.expectEdit(t, "1: ↺ Taxes · <b>auto</b> · p1")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t)
	telegram.message(42, "/sessions")
	telegram.expect(t, "Recent sessions. Pick one to continue it:\n\n1. Taxes, 3h ago  ← current\n2. Trip, 2h ago")
	telegram.expectButtons(t, "s:aaa", "s:bbb", "sj:", "x:")

	// Job runs have a list of their own, and the chats come back.
	telegram.press("sj:")
	telegram.expectEdit(t, "1: Recent job runs. Pick one to continue it:\n\n1. News alerts, 1h ago")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "s:job", "sc:", "x:")
	telegram.press("sc:")
	telegram.expectEdit(t, "1: Recent sessions. Pick one to continue it:\n\n1. Taxes, 3h ago  ← current\n2. Trip, 2h ago")
	telegram.expectAnswer(t, "")
	telegram.expectButtons(t, "s:aaa", "s:bbb", "sj:", "x:")

	// Words find sessions of either kind by title.
	telegram.message(42, "/sessions ALERTS")
	telegram.expect(t, "Sessions with “ALERTS”. Pick one to continue it:\n\n1. ⏰ News alerts, 1h ago")
	telegram.expectButtons(t, "s:job", "x:")
	telegram.message(42, "/sessions boat")
	telegram.expect(t, "No session title has “boat”.")
}

func TestProgressNote(t *testing.T) {
	cfg, telegram := startChat(t, nil)
	c := New(cfg, nil, usage.Open(cfg.State))
	c.turnID = "t1"

	// A note from before the turn is not shown.
	c.Note("t1", "stale")
	lines, done := make(chan string, 4), make(chan struct{})
	shown := make(chan struct{})
	go func() {
		c.showProgress(context.Background(), lines, done)
		close(shown)
	}()
	defer func() {
		close(done)
		<-shown
	}()

	lines <- "Running den map -p @prompt.md jobs.jsonl"
	time.Sleep(50 * time.Millisecond)
	// A job's agent, or one between turns, is not this turn.
	c.Note("", "🗂 from a job")
	c.Note("t0", "🗂 from an old turn")
	c.Note("t1", "🗂 3 of 10 items")
	telegram.expect(t, "Working…\n→ Running den map…\n   🗂 3 of 10 items")

	// The next step ends the note.
	lines <- "Reading jobs.out.jsonl"
	select {
	case got := <-telegram.edited:
		if strings.Contains(got, "🗂") || !strings.Contains(got, "jobs.out.jsonl") {
			t.Fatalf("edited %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the progress message was not edited")
	}
}

func TestTell(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg, nil, usage.Open(cfg.State))
	c.turns = []turnRef{{"t1", 0}}

	if c.Tell("job", "[den: done]", "done") {
		t.Fatal("a turn the chat did not run took the note")
	}
	if !c.Tell("t1", "[den: done]", "done") || len(c.inbox) != 1 || c.inbox[0].text != "[den: done]" {
		t.Fatalf("inbox %+v", c.inbox)
	}

	// A new session is not the one the command's turn ran in: a report
	// still waiting goes to the user, and the user's own message stays.
	c.inbox = append(c.inbox, entry{text: "hi"})
	notices := c.switchTo("")
	if !slices.Equal(notices, []string{"done"}) || len(c.inbox) != 1 || c.inbox[0].text != "hi" {
		t.Fatalf("notices %q, inbox %+v", notices, c.inbox)
	}
	if c.Tell("t1", "[den: done]", "done") {
		t.Fatal("a turn of the old session took the note")
	}
}
