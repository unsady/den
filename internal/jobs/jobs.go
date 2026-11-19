// Package jobs holds the rules of scheduled jobs: their names, titles,
// schedules, and models. den's store keeps them and their runs.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/unsady/den/internal/schedule"
	"github.com/unsady/den/internal/store"
)

type (
	Job = store.Job
	Run = store.Run
)

// The chat's two models a job can run on.
const (
	Main   = "main"
	Second = "second"
)

// slot checks a slot's name and returns how a job stores it.
func slot(name string) (string, error) {
	switch name {
	case "", Main:
		return "", nil
	case Second:
		return Second, nil
	}
	return "", fmt.Errorf("a job runs on the main or the second model, not %q", name)
}

// namePattern is a job's name: two or three English words, lowercase,
// joined by "-", without digits.
var namePattern = regexp.MustCompile(`^[a-z]+(-[a-z]+){1,2}$`)

// CheckName says why name is not a job's name, or nil when it is one.
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("a job's name is 2 or 3 lowercase English words joined by \"-\", without digits, such as morning-brief, not %q", name)
	}
	return nil
}

// maxTitle is the most runes a job's title has.
const maxTitle = 80

// checkTitle says why title is not a job's title, or nil when it is one.
func checkTitle(title string) error {
	switch {
	case title == "":
		return errors.New("a job needs a title, a few words in the user's language")
	case strings.ContainsAny(title, "\r\n"):
		return errors.New("a job's title is one line")
	case len([]rune(title)) > maxTitle:
		return fmt.Errorf("a job's title is at most %d characters", maxTitle)
	}
	return nil
}

var ErrNotFound = errors.New("no such job")

// Store keeps jobs and their runs in den's store.
type Store struct {
	db *store.Store
}

func New(db *store.Store) *Store { return &Store{db: db} }

// List returns every job, soonest first.
func (s *Store) List() ([]Job, error) {
	return s.db.Jobs(context.Background())
}

// Get finds a job by name.
func (s *Store) Get(name string) (Job, error) {
	j, err := s.db.Job(context.Background(), name)
	if errors.Is(err, store.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// Add checks j's name, title, schedule, and slot, fills its first run, and
// saves it.
func (s *Store) Add(j Job, now time.Time) (Job, error) {
	j.Name = strings.TrimSpace(j.Name)
	j.Title = strings.TrimSpace(j.Title)
	j.Prompt = strings.TrimSpace(j.Prompt)
	if err := CheckName(j.Name); err != nil {
		return Job{}, err
	}
	if err := checkTitle(j.Title); err != nil {
		return Job{}, err
	}
	if j.Prompt == "" {
		return Job{}, errors.New("a job needs a prompt")
	}

	next, once, err := schedule.First(j.Schedule, now)
	if err != nil {
		return Job{}, err
	}
	if j.Slot, err = slot(j.Slot); err != nil {
		return Job{}, err
	}

	j.NextRun = next
	j.Once = once
	j.Created = now
	j.LastRun = nil
	j.LastStatus = ""
	if err := s.db.AddJob(context.Background(), j); err != nil {
		if errors.Is(err, store.ErrJobExists) {
			return Job{}, fmt.Errorf("a job named %q already exists", j.Name)
		}
		return Job{}, err
	}
	return j, nil
}

func (s *Store) Remove(name string) (Job, error) {
	j, err := s.db.RemoveJob(context.Background(), name)
	if errors.Is(err, store.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// TakeDue returns the jobs due at now that skip does not exclude. It moves
// each recurring one to its next run and removes each one-shot before it
// runs, so a crash mid-run never repeats a job.
func (s *Store) TakeDue(now time.Time, skip func(name string) bool) ([]Job, error) {
	return s.db.TakeDueJobs(context.Background(), now, skip, func(j Job) (time.Time, error) {
		return schedule.Next(j.Schedule, now)
	})
}

// SetSlot makes a job run on the main or the second model.
func (s *Store) SetSlot(name, slotName string) (Job, error) {
	stored, err := slot(slotName)
	if err != nil {
		return Job{}, err
	}
	j, err := s.db.SetJobSlot(context.Background(), name, stored)
	if errors.Is(err, store.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// Finish records how a run ended and keeps its record.
func (s *Store) Finish(run Run) error {
	ctx := context.Background()
	if err := s.db.AddRun(ctx, run); err != nil {
		return err
	}
	return s.db.FinishJob(ctx, run.Job, run.Started, run.Status())
}

// Runs returns up to n of a job's latest runs, newest first.
func (s *Store) Runs(name string, n int) ([]Run, error) {
	return s.db.Runs(context.Background(), name, n)
}
