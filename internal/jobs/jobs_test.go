package jobs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTakeDue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	store, created, err := Open(path)
	if err != nil || !created {
		t.Fatalf("open: created=%v err=%v", created, err)
	}

	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)
	once, _ := store.Add(Job{Schedule: "in 1m", Prompt: "remind"}, now)
	daily, _ := store.Add(Job{Name: "daily", Schedule: "0 9 * * *", Prompt: "plan"}, now)

	if due, _ := store.TakeDue(now, func(string) bool { return false }); len(due) != 0 {
		t.Fatalf("nothing is due yet, got %d", len(due))
	}

	later := now.Add(24 * time.Hour)
	due, err := store.TakeDue(later, func(string) bool { return false })
	if err != nil || len(due) != 2 {
		t.Fatalf("want both jobs due, got %d err=%v", len(due), err)
	}

	if _, err := store.Get(once.ID); err != ErrNotFound {
		t.Errorf("a one-shot job is removed when it is taken")
	}

	reopened, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("daily")
	if err != nil || got.ID != daily.ID {
		t.Fatalf("get by name: %v", err)
	}
	if want := time.Date(2026, 11, 11, 9, 0, 0, 0, time.Local); !got.NextRun.Equal(want) {
		t.Errorf("next run %v, want %v", got.NextRun, want)
	}
}

func TestSlot(t *testing.T) {
	store, _, _ := Open(filepath.Join(t.TempDir(), "jobs.json"))
	now := time.Now()

	j, err := store.Add(Job{Name: "x", Schedule: "every 1h", Prompt: "a", Slot: Main}, now)
	if err != nil || j.Slot != "" {
		t.Fatalf("main is stored empty: slot=%q err=%v", j.Slot, err)
	}
	if _, err := store.Add(Job{Name: "y", Schedule: "every 1h", Prompt: "a", Slot: "gpt"}, now); err == nil {
		t.Error("want an error for a slot that is not main or second")
	}

	if j, err = store.SetSlot("x", Second); err != nil || j.Slot != Second {
		t.Fatalf("set second: slot=%q err=%v", j.Slot, err)
	}
	if j, err = store.SetSlot("x", Main); err != nil || j.Slot != "" {
		t.Fatalf("set main: slot=%q err=%v", j.Slot, err)
	}
	if _, err := store.SetSlot("nope", Second); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestAddRejectsDuplicateNames(t *testing.T) {
	store, _, _ := Open(filepath.Join(t.TempDir(), "jobs.json"))
	now := time.Now()

	if _, err := store.Add(Job{Name: "x", Schedule: "every 1h", Prompt: "a"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Job{Name: "x", Schedule: "every 1h", Prompt: "b"}, now); err == nil {
		t.Error("want an error for a duplicate name")
	}
}

func TestRunSessions(t *testing.T) {
	runs := NewRuns(t.TempDir())
	now := time.Now()
	for i, r := range []Run{{JobID: "a", SessionID: "s1"}, {JobID: "a", SessionID: "s2"}, {JobID: "b", SessionID: "s3"}, {JobID: "b"}} {
		r.Started = now.Add(time.Duration(i) * time.Second)
		if err := runs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	got := runs.Sessions()
	if len(got) != 3 || !got["s1"] || !got["s2"] || !got["s3"] {
		t.Fatalf("sessions %v", got)
	}
}

func TestRunPruneForgets(t *testing.T) {
	runs := NewRuns(t.TempDir())
	runs.keep = 1
	var forgot []string
	runs.Forget = func(session string) { forgot = append(forgot, session) }
	now := time.Now()
	for i, id := range []string{"s1", "s2", "s3"} {
		if err := runs.Save(Run{JobID: "a", SessionID: id, Started: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(forgot, " ") != "s1 s2" {
		t.Fatalf("forgot %q", forgot)
	}
	// A pruned run's session is still a job's.
	if got := runs.Sessions(); len(got) != 3 || !got["s1"] {
		t.Fatalf("sessions %v", got)
	}
}
