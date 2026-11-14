package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
)

func TestJobsWaitForASlot(t *testing.T) {
	d := &daemon{ctx: context.Background(), slots: make(chan struct{}, jobSlots(config.Config{Jobs: 1}))}

	if !d.acquire(jobs.Job{ID: "a"}) {
		t.Fatal("the first job did not get the slot")
	}

	got := make(chan bool)
	go func() { got <- d.acquire(jobs.Job{ID: "b"}) }()
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
	ctx, stop := context.WithCancel(context.Background())
	d := &daemon{ctx: ctx, slots: make(chan struct{}, 1)}
	d.slots <- struct{}{}

	got := make(chan bool)
	go func() { got <- d.acquire(jobs.Job{ID: "b"}) }()
	time.Sleep(50 * time.Millisecond)
	stop()

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
