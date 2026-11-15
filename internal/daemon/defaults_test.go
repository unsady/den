package daemon

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/unsady/den/internal/jobs"
)

func names(store *jobs.Store) []string {
	var out []string
	for _, j := range store.List() {
		out = append(out, j.Name)
	}
	slices.Sort(out)
	return out
}

func TestAddDefaultJobs(t *testing.T) {
	// A new store gets every default job.
	dir := t.TempDir()
	store, created, err := jobs.Open(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "default-jobs")
	if err := addDefaultJobs(store, list, created); err != nil {
		t.Fatal(err)
	}
	if got := names(store); !slices.Equal(got, []string{"reflect", "tidy"}) {
		t.Fatalf("new store: %v", got)
	}

	// A removed one stays removed.
	if _, err := store.Remove("tidy"); err != nil {
		t.Fatal(err)
	}
	if err := addDefaultJobs(store, list, false); err != nil {
		t.Fatal(err)
	}
	if got := names(store); !slices.Equal(got, []string{"reflect"}) {
		t.Fatalf("after removal: %v", got)
	}

	// A store from before the list gets only the new job, even when the user
	// removed reflect.
	dir = t.TempDir()
	store, _, err = jobs.Open(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(jobs.Job{Name: "news", Schedule: "@daily", Prompt: "Use the news skill."}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := addDefaultJobs(store, filepath.Join(dir, "default-jobs"), false); err != nil {
		t.Fatal(err)
	}
	if got := names(store); !slices.Equal(got, []string{"news", "tidy"}) {
		t.Fatalf("old store: %v", got)
	}
}
