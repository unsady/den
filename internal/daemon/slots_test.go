package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsady/den/internal/chat"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/store"
	"github.com/unsady/den/internal/usage"
)

func TestJobsWaitForASlot(t *testing.T) {
	d := &daemon{ctx: context.Background(), slots: make(chan struct{}, jobSlots(config.Config{Jobs: 1}))}

	if !d.acquire(jobs.Job{Name: "a"}) {
		t.Fatal("the first job did not get the slot")
	}

	got := make(chan bool)
	go func() { got <- d.acquire(jobs.Job{Name: "b"}) }()
	select {
	case <-got:
		t.Fatal("the second job started while the first ran")
	case <-time.After(100 * time.Millisecond):
	}

	<-d.slots
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("the second job did not start after the first ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second job never started")
	}
}

func TestWaitingJobStopsOnShutdown(t *testing.T) {
	stopping := make(chan struct{})
	d := &daemon{ctx: context.Background(), stopping: stopping, slots: make(chan struct{}, 1)}
	d.slots <- struct{}{}

	got := make(chan bool)
	go func() { got <- d.acquire(jobs.Job{Name: "b"}) }()
	time.Sleep(50 * time.Millisecond)
	close(stopping)

	select {
	case ok := <-got:
		if ok {
			t.Fatal("a waiting job ran after shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting job did not stop")
	}
}

func TestJobSlotsDefault(t *testing.T) {
	if n := jobSlots(config.Config{}); n != 2 {
		t.Errorf("default = %d, want 2", n)
	}
}

// TestDrain waits for a running job to end, and no longer than its limit.
func TestDrain(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	db, err := store.Open(filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &daemon{ctx: context.Background(), running: map[string]bool{"news": true}}
	d.chat = chat.New(cfg, d, usage.Open(cfg.State), db)

	go func() {
		time.Sleep(100 * time.Millisecond)
		d.mu.Lock()
		delete(d.running, "news")
		d.mu.Unlock()
	}()
	start := time.Now()
	d.drain(time.Minute)
	if waited := time.Since(start); waited < 100*time.Millisecond || waited > 5*time.Second {
		t.Errorf("waited %s for a job that ended after 100ms", waited)
	}

	d.running["slow"] = true
	start = time.Now()
	d.drain(200 * time.Millisecond)
	if waited := time.Since(start); waited > 3*time.Second {
		t.Errorf("waited %s past a limit of 200ms", waited)
	}
}
