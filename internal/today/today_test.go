package today

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
)

const list = "# Today\n\n- [ ] Buy oat milk\n- [x] Call the bank\n  * [ ] Ask about the card\n\n```\n- [ ] not a to-do\n```\n- [ ]   \nSome text.\n"

func TestTasks(t *testing.T) {
	cfg := config.Config{Space: t.TempDir()}
	tasks, version, err := Tasks(cfg)
	if err != nil || len(tasks) != 0 || version != "" {
		t.Fatalf("no list: %v %q %v", tasks, version, err)
	}

	if err := os.WriteFile(filepath.Join(cfg.Space, File), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, version, err = Tasks(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []Task{{"Buy oat milk", false}, {"Call the bank", true}, {"Ask about the card", false}}
	if len(tasks) != len(want) || version == "" {
		t.Fatalf("Tasks = %+v %q", tasks, version)
	}
	for i := range want {
		if tasks[i] != want[i] {
			t.Errorf("task %d = %+v, want %+v", i, tasks[i], want[i])
		}
	}
}

func TestCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	space := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = space
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "den")
	git("config", "user.email", "den@localhost")
	path := filepath.Join(space, File)
	if err := os.WriteFile(path, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "Plan the day")
	cfg := config.Config{Space: space}

	_, version, _ := Tasks(cfg)
	if err := Check(cfg, 2, true, version); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if want := "# Today\n\n- [ ] Buy oat milk\n- [x] Call the bank\n  * [x] Ask about the card\n\n```\n- [ ] not a to-do\n```\n- [ ]   \nSome text.\n"; string(data) != want {
		t.Fatalf("file = %q", data)
	}
	if got := git("log", "-1", "--name-only", "--format=%s"); got != "Tick a to-do in the app\n\ntoday.md\n" {
		t.Errorf("commit = %q", got)
	}

	if err := Check(cfg, 1, false, version); !errors.Is(err, ErrChanged) {
		t.Errorf("stale version: %v", err)
	}
	_, version, _ = Tasks(cfg)
	if err := Check(cfg, 3, true, version); !errors.Is(err, ErrNoTask) {
		t.Errorf("past the list: %v", err)
	}
	if err := Check(cfg, 1, false, version); err != nil {
		t.Fatal(err)
	}
	if tasks, _, _ := Tasks(cfg); tasks[1].Done {
		t.Error("the bank stayed ticked")
	}
}

func TestNight(t *testing.T) {
	loc := time.FixedZone("NY", -5*3600)
	from, to := Night(time.Date(2026, 3, 1, 9, 30, 0, 0, loc))
	if !from.Equal(time.Date(2026, 2, 28, 23, 0, 0, 0, loc)) || !to.Equal(time.Date(2026, 3, 1, 8, 0, 0, 0, loc)) {
		t.Errorf("night = %s – %s", from, to)
	}
	if !Quiet(from) || Quiet(to) || !Quiet(to.Add(-time.Minute)) {
		t.Error("quiet hours")
	}
}

func TestEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("singleEvents") != "true" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"items":[
			{"summary":"Holiday","start":{"date":"2026-11-23"},"end":{"date":"2026-11-24"}},
			{"summary":"Standup","start":{"dateTime":"2026-11-23T14:00:00Z"},"end":{"dateTime":"2026-11-23T14:15:00Z"}},
			{"summary":"Gone","status":"cancelled","start":{"dateTime":"2026-11-23T15:00:00Z"},"end":{"dateTime":"2026-11-23T16:00:00Z"}},
			{"summary":"Declined","attendees":[{"self":true,"responseStatus":"declined"}],"start":{"dateTime":"2026-11-23T16:00:00Z"},"end":{"dateTime":"2026-11-23T17:00:00Z"}},
			{"start":{"dateTime":"2026-11-23T18:00:00Z"},"end":{"dateTime":"2026-11-23T19:00:00Z"}}
		]}`))
	}))
	defer srv.Close()
	calendarURL = srv.URL

	loc := time.FixedZone("NY", -5*3600)
	from, to := Bounds(time.Date(2026, 11, 23, 10, 0, 0, 0, loc))
	events, err := Events(context.Background(), srv.Client(), "tok", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if e := events[0]; e.Title != "Holiday" || !e.AllDay || !e.Start.Equal(from) {
		t.Errorf("all-day = %+v", e)
	}
	if e := events[1]; e.Title != "Standup" || e.AllDay || e.Start.Hour() != 9 {
		t.Errorf("timed = %+v", e)
	}
	if events[2].Title != "(no title)" {
		t.Errorf("untitled = %+v", events[2])
	}

	if _, err := Events(context.Background(), srv.Client(), "bad", from, to); err == nil {
		t.Error("a refused token read events")
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n\n  Rain at noon.  \nMore."); got != "Rain at noon." {
		t.Errorf("FirstLine = %q", got)
	}
}
