package events

import (
	"errors"
	"os"
	"testing"
)

func TestJournal(t *testing.T) {
	h := &Hub{Dir: t.TempDir()}

	// The messages to a new session wait for the reply that names it.
	h.Publish(Event{Type: Message, Text: "hello"})
	h.Publish(Event{Type: Step, Session: "s1", Text: "Reading notes.md"})
	h.Publish(Event{Type: Reply, Session: "s1", Text: "hi"})
	h.Publish(Event{Type: Idle, Session: "s1"})
	h.Publish(Event{Type: Result, Session: "job1", Title: "morning", Text: "Three things"})

	list, err := Journal(h.Dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Type != Message || list[0].Text != "hello" || list[1].Text != "hi" || list[1].Session != "s1" {
		t.Fatalf("journal %+v", list)
	}

	if list, _ := Journal(h.Dir, "job1"); len(list) != 1 || list[0].Title != "morning" {
		t.Fatalf("job journal %+v", list)
	}

	h.Forget("job1")
	if _, err := Journal(h.Dir, "job1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten: %v", err)
	}
}
