// Package jobs stores scheduled jobs and their run records.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/schedule"
)

type Job struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	// Slot is which of the chat's two models the job runs on: Second, or
	// empty for the main one.
	Slot    string    `json:"slot,omitempty"`
	Silent  bool      `json:"silent,omitempty"`
	Once    bool      `json:"once,omitempty"`
	NextRun time.Time `json:"next_run"`
	Created time.Time `json:"created"`

	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
}

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

// Label names the job for people: its name, or its id.
func (j Job) Label() string {
	if j.Name != "" {
		return j.Name
	}
	return j.ID
}

var ErrNotFound = errors.New("no such job")

// Store keeps jobs in one JSON file. Every change is written before it
// returns.
type Store struct {
	mu   sync.Mutex
	path string
	jobs []Job
}

// Open loads the store at path. created reports that the file did not exist.
func Open(path string) (store *Store, created bool, err error) {
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, true, s.save()
	}
	if err != nil {
		return nil, false, err
	}

	if err := json.Unmarshal(data, &s.jobs); err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return s, false, nil
}

// List returns every job, soonest first.
func (s *Store) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := append([]Job(nil), s.jobs...)
	sort.Slice(out, func(a, b int) bool { return out[a].NextRun.Before(out[b].NextRun) })
	return out
}

// Get finds a job by id, or by name when no id matches.
func (s *Store) Get(ref string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.find(ref)
	if i < 0 {
		return Job{}, ErrNotFound
	}
	return s.jobs[i], nil
}

// Add validates j's schedule, fills its id and first run, and saves it.
func (s *Store) Add(j Job, now time.Time) (Job, error) {
	j.Prompt = strings.TrimSpace(j.Prompt)
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

	s.mu.Lock()
	defer s.mu.Unlock()

	if j.Name != "" && s.find(j.Name) >= 0 {
		return Job{}, fmt.Errorf("a job named %q already exists", j.Name)
	}

	j.ID = newID()
	j.NextRun = next
	j.Once = once
	j.Created = now
	j.LastRun = nil
	j.LastStatus = ""

	s.jobs = append(s.jobs, j)
	return j, s.save()
}

func (s *Store) Remove(ref string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.find(ref)
	if i < 0 {
		return Job{}, ErrNotFound
	}

	j := s.jobs[i]
	s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
	return j, s.save()
}

// TakeDue returns the jobs due at now that skip does not exclude. It moves
// each recurring one to its next run and removes each one-shot before it
// runs, so a crash mid-run never repeats a job.
func (s *Store) TakeDue(now time.Time, skip func(id string) bool) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var due []Job
	kept := s.jobs[:0]
	for _, j := range s.jobs {
		if j.NextRun.After(now) || skip(j.ID) {
			kept = append(kept, j)
			continue
		}

		due = append(due, j)
		if j.Once {
			continue
		}

		next, err := schedule.Next(j.Schedule, now)
		if err != nil {
			next = now.Add(time.Hour)
			j.LastStatus = err.Error()
		}
		j.NextRun = next
		kept = append(kept, j)
	}
	s.jobs = kept

	if len(due) == 0 {
		return nil, nil
	}
	return due, s.save()
}

// SetSlot makes a job run on the main or the second model.
func (s *Store) SetSlot(ref, name string) (Job, error) {
	stored, err := slot(name)
	if err != nil {
		return Job{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.find(ref)
	if i < 0 {
		return Job{}, ErrNotFound
	}
	s.jobs[i].Slot = stored
	return s.jobs[i], s.save()
}

// Finish records the outcome of a run on the job, if it still exists.
func (s *Store) Finish(id string, at time.Time, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.find(id)
	if i < 0 {
		return nil
	}

	s.jobs[i].LastRun = &at
	s.jobs[i].LastStatus = status
	return s.save()
}

func (s *Store) find(ref string) int {
	for i, j := range s.jobs {
		if j.ID == ref {
			return i
		}
	}
	for i, j := range s.jobs {
		if j.Name != "" && j.Name == ref {
			return i
		}
	}
	return -1
}

func (s *Store) save() error {
	jobs := s.jobs
	if jobs == nil {
		jobs = []Job{}
	}

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.path, append(data, '\n'))
}

func newID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writeFile replaces path atomically with owner-only permissions.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
