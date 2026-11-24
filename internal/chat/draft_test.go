package chat

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestDrafter(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	d := &drafter{publish: func(text string) {
		mu.Lock()
		sent = append(sent, text)
		mu.Unlock()
	}}
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}

	// The first text goes at once, and what follows soon after once the
	// time is up.
	d.set("I'll")
	d.set("I'll write")
	d.set(" I'll write it ")
	if s := got(); !slices.Equal(s, []string{"I'll"}) {
		t.Fatalf("sent %q", s)
	}
	time.Sleep(2 * draftEvery)
	if s := got(); !slices.Equal(s, []string{"I'll", "I'll write it"}) {
		t.Fatalf("sent %q", s)
	}

	// A step takes it at once, whether the text before it went or waited
	// for its time; nothing goes once the turn ended.
	d.set("Next")
	d.set("")
	d.end()
	d.set("late")
	time.Sleep(2 * draftEvery)
	if s := got(); s[len(s)-1] != "" || slices.Contains(s, "late") {
		t.Fatalf("sent %q", s)
	}
}
