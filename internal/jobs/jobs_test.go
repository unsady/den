package jobs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsady/den/internal/store"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), store.File))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func TestTakeDue(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)
	if _, err := s.Add(Job{Name: "call-the-bank", Title: "Позвонить в банк", Schedule: "in 1m", Prompt: "remind"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Job{Name: "daily-plan", Title: "Daily plan", Schedule: "0 9 * * *", Prompt: "plan"}, now); err != nil {
		t.Fatal(err)
	}

	never := func(string) bool { return false }
	if due, _ := s.TakeDue(now, never); len(due) != 0 {
		t.Fatalf("nothing is due yet, got %d", len(due))
	}

	later := now.Add(24 * time.Hour)
	due, err := s.TakeDue(later, never)
	if err != nil || len(due) != 2 {
		t.Fatalf("want both jobs due, got %d err=%v", len(due), err)
	}
	if _, err := s.Get("call-the-bank"); err != ErrNotFound {
		t.Errorf("a one-shot job is removed when it is taken")
	}
	got, err := s.Get("daily-plan")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local); !got.NextRun.Equal(want) {
		t.Errorf("next run %v, want %v", got.NextRun, want)
	}

	// A running job is skipped and stays due.
	if due, _ := s.TakeDue(got.NextRun, func(name string) bool { return name == "daily-plan" }); len(due) != 0 {
		t.Errorf("a skipped job was taken")
	}
}

func TestName(t *testing.T) {
	for _, name := range []string{"morning-brief", "news-alerts", "call-the-bank"} {
		if err := CheckName(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"", "reflect", "a-b-c-d", "news-2", "News-alerts", "news_alerts", "news--alerts", "-news-alerts", "новости-утро"} {
		if CheckName(name) == nil {
			t.Errorf("%q passed", name)
		}
	}
}

func TestAddChecks(t *testing.T) {
	s := open(t)
	now := time.Now()
	ok := Job{Name: "hourly-check", Title: "Check", Schedule: "every 1h", Prompt: "a"}
	if _, err := s.Add(ok, now); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		change func(*Job)
		want   string
	}{
		{func(j *Job) {}, "already exists"},
		{func(j *Job) { j.Name = "check" }, "2 or 3"},
		{func(j *Job) { j.Name, j.Title = "other-check", "" }, "title"},
		{func(j *Job) { j.Name, j.Title = "other-check", "two\nlines" }, "one line"},
		{func(j *Job) { j.Name, j.Prompt = "other-check", " " }, "prompt"},
		{func(j *Job) { j.Name, j.Slot = "other-check", "gpt" }, "main or the second"},
	} {
		j := ok
		tc.change(&j)
		if _, err := s.Add(j, now); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", j, err, tc.want)
		}
	}
}

func TestEdit(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)
	added, err := s.Add(Job{Name: "hourly-check", Title: "Проверка", Schedule: "every 1h", Prompt: "a", Slot: Main}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(Run{Job: "hourly-check", Started: now}); err != nil {
		t.Fatal(err)
	}

	ptr := func(s string) *string { return &s }
	later := now.Add(time.Minute)
	j, err := s.Edit("hourly-check", Change{Title: ptr(" Check "), Slot: ptr(Second)}, later)
	if err != nil {
		t.Fatal(err)
	}
	if j.Title != "Check" || j.Slot != Second || j.Prompt != "a" || !j.NextRun.Equal(added.NextRun) {
		t.Fatalf("title and slot edit: %+v", j)
	}
	if j, _ := s.Get("hourly-check"); j.LastRun == nil || !j.Created.Equal(added.Created) {
		t.Errorf("edit lost the job's history: %+v", j)
	}

	j, err = s.Edit("hourly-check", Change{Schedule: ptr("in 5m"), Slot: ptr(Main)}, later)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Once || !j.NextRun.Equal(later.Add(5*time.Minute)) || j.Slot != "" {
		t.Fatalf("schedule edit: %+v", j)
	}
	if j, err := s.Edit("hourly-check", Change{Schedule: ptr("in 5m")}, later.Add(time.Minute)); err != nil || !j.NextRun.Equal(later.Add(5*time.Minute)) {
		t.Fatalf("the same schedule moved the next run: %+v %v", j, err)
	}

	for _, c := range []Change{{Title: ptr("")}, {Prompt: ptr(" ")}, {Schedule: ptr("whenever")}, {Slot: ptr("third")}} {
		if _, err := s.Edit("hourly-check", c, later); err == nil {
			t.Errorf("%+v: want an error", c)
		}
	}
	if j, _ := s.Get("hourly-check"); j.Title != "Check" {
		t.Errorf("a failed edit changed the job: %+v", j)
	}
	if _, err := s.Edit("no-such-job", Change{Slot: ptr(Second)}, later); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestRuns(t *testing.T) {
	s := open(t)
	now := time.Now().Truncate(time.Millisecond)
	if _, err := s.Add(Job{Name: "hourly-check", Title: "Check", Schedule: "every 1h", Prompt: "a"}, now); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		run := Run{Job: "hourly-check", Started: now.Add(time.Duration(i) * time.Second), Duration: 1500 * time.Millisecond, Output: "out"}
		if i == 2 {
			run.Error = "boom"
		}
		if err := s.Finish(run); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := s.Runs("hourly-check", 2)
	if err != nil || len(runs) != 2 || runs[0].Error != "boom" || runs[0].Duration != 1500*time.Millisecond {
		t.Fatalf("runs %+v %v", runs, err)
	}
	j, _ := s.Get("hourly-check")
	if j.LastStatus != "failed" || j.LastRun == nil || !j.LastRun.Equal(now.Add(2*time.Second)) {
		t.Errorf("last run %v %q", j.LastRun, j.LastStatus)
	}
}
