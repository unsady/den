package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/store"
)

func names(t *testing.T, js *jobs.Store) []string {
	t.Helper()
	list, err := js.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, j := range list {
		out = append(out, j.Name)
	}
	slices.Sort(out)
	return out
}

func openJobs(t *testing.T) (*store.Store, *jobs.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), store.File))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, jobs.New(db)
}

func TestAddDefaultJobs(t *testing.T) {
	ctx := context.Background()
	db, js := openJobs(t)

	// A new store gets every default job.
	if err := addDefaultJobs(ctx, db, js); err != nil {
		t.Fatal(err)
	}
	if got := names(t, js); !slices.Equal(got, []string{reflectJob, tidyJob}) {
		t.Fatalf("new store: %v", got)
	}

	// A removed one stays removed.
	if _, err := js.Remove(tidyJob); err != nil {
		t.Fatal(err)
	}
	if err := addDefaultJobs(ctx, db, js); err != nil {
		t.Fatal(err)
	}
	if got := names(t, js); !slices.Equal(got, []string{reflectJob}) {
		t.Fatalf("after removal: %v", got)
	}

	// A user's job by a default's name stays theirs.
	db, js = openJobs(t)
	if _, err := js.Add(jobs.Job{Name: tidyJob, Title: "Mine", Schedule: "@daily", Prompt: "Mine."}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := addDefaultJobs(ctx, db, js); err != nil {
		t.Fatal(err)
	}
	if j, _ := js.Get(tidyJob); j.Title != "Mine" {
		t.Fatalf("the user's job became %+v", j)
	}
}
