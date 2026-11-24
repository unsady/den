package chat

import (
	"slices"
	"strings"
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

	long := strings.Repeat("я", draftStart)

	// A short text, such as what the agent says before a step, does not go;
	// the first long one goes at once, and what follows soon after once the
	// time is up.
	d.set("I'll check")
	if s := got(); len(s) != 0 {
		t.Fatalf("sent %q", s)
	}
	d.set(long)
	d.set(long + " and")
	d.set(" " + long + " and more ")
	if s := got(); !slices.Equal(s, []string{long}) {
		t.Fatalf("sent %q", s)
	}
	time.Sleep(2 * draftEvery)
	if s := got(); !slices.Equal(s, []string{long, long + " and more"}) {
		t.Fatalf("sent %q", s)
	}

	// A step takes it at once, whether the text before it went or waited
	// for its time; nothing goes once the turn ended.
	d.set(long + " next")
	d.set("")
	d.end()
	d.set("late")
	time.Sleep(2 * draftEvery)
	if s := got(); len(s) < 3 || s[len(s)-1] != "" || slices.Contains(s, "late") {
		t.Fatalf("sent %q", s)
	}
}
