package events

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestJournal(t *testing.T) {
	h := &Hub{Dir: t.TempDir()}

	h.Publish(Event{Type: Message, Session: "s1", Text: "hello"})
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
	if list[0].Key == "" || list[0].Key == list[1].Key {
		t.Fatalf("keys %q and %q", list[0].Key, list[1].Key)
	}

	if list, _ := Journal(h.Dir, "job1"); len(list) != 1 || list[0].Title != "morning" {
		t.Fatalf("job journal %+v", list)
	}

	h.Forget("job1")
	if _, err := Journal(h.Dir, "job1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten: %v", err)
	}
}

func TestSearch(t *testing.T) {
	h := &Hub{Dir: t.TempDir()}
	at := time.Now()
	h.Publish(Event{Type: Message, Session: "s1", Text: "Book a table in Lisbon", At: at})
	h.Publish(Event{Type: Reply, Session: "s1", Text: "Booked: Taberna, 8 pm", At: at.Add(time.Second)})
	h.Publish(Event{Type: Step, Session: "s1", Text: "Reading lisbon.md"})
	h.Publish(Event{Type: Reply, Session: "s2", Text: "Lisbon flights are cheaper on Tuesday", At: at.Add(2 * time.Second)})

	all := func(string) bool { return true }
	found, err := Search(context.Background(), h.Dir, "  LISBON ", 10, all)
	if err != nil || len(found) != 2 || found[0].Session != "s2" || found[1].Text != "Book a table in Lisbon" {
		t.Fatalf("found %+v, %v", found, err)
	}
	if found, _ := Search(context.Background(), h.Dir, "lisbon table", 10, all); len(found) != 1 || found[0].Session != "s1" {
		t.Fatalf("both words %+v", found)
	}
	if found, _ := Search(context.Background(), h.Dir, "lisbon", 1, all); len(found) != 1 || found[0].Session != "s2" {
		t.Fatalf("limit %+v", found)
	}
	if found, _ := Search(context.Background(), h.Dir, " ", 10, all); len(found) != 0 {
		t.Fatalf("no words %+v", found)
	}
	// A session left out takes no place in the limit.
	only := func(s string) bool { return s == "s1" }
	if found, _ := Search(context.Background(), h.Dir, "lisbon", 1, only); len(found) != 1 || found[0].Session != "s1" {
		t.Fatalf("kept %+v", found)
	}
}

func TestFile(t *testing.T) {
	h := &Hub{Dir: t.TempDir()}

	file, err := h.SaveFile("s1", "/tmp/Chart.PNG", []byte("png"))
	if err != nil || !strings.HasSuffix(file, ".png") {
		t.Fatalf("saved %q, %v", file, err)
	}
	if data, err := os.ReadFile(h.FilePath("s1", file)); err != nil || string(data) != "png" {
		t.Fatalf("file %q, %v", data, err)
	}

	h.Publish(Event{Type: Reply, Session: "s1", Text: "Speed", Files: []Attachment{{File: file, Name: "Chart.PNG", Size: 3}}})
	list, err := Journal(h.Dir, "s1")
	if err != nil || len(list) != 1 || len(list[0].Files) != 1 || list[0].Files[0].File != file {
		t.Fatalf("journal %+v, %v", list, err)
	}

	h.Forget("s1")
	if _, err := os.Stat(h.FilePath("s1", file)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten: %v", err)
	}
}

// TestSlowListener checks that a listener that falls behind is dropped, so
// it knows it missed events, while the others go on.
func TestSlowListener(t *testing.T) {
	var h Hub
	slow, stopSlow := h.Listen()
	fast, stopFast := h.Listen()
	defer stopFast()

	for range buffer + 1 {
		h.Publish(Event{Type: Step, Session: "s1"})
		<-fast
	}

	n := 0
	for range slow {
		n++
	}
	if n != buffer {
		t.Fatalf("the slow listener got %d events, then its channel should close", n)
	}
	// Stopping a dropped listener does nothing.
	stopSlow()

	h.Publish(Event{Type: Idle, Session: "s1"})
	if e := <-fast; e.Type != Idle {
		t.Fatalf("the fast listener got %+v", e)
	}
}

func TestSlowListenerMissesDrafts(t *testing.T) {
	var h Hub
	list, stop := h.Listen()
	defer stop()

	for range buffer * 2 {
		h.Publish(Event{Type: Draft, Session: "s1", Text: "more"})
	}
	for range buffer {
		<-list
	}
	select {
	case _, open := <-list:
		t.Fatalf("got more, open %v", open)
	default:
	}

	// Anything else it has no room for drops it.
	for range buffer + 1 {
		h.Publish(Event{Type: Step, Session: "s1"})
	}
	for range buffer {
		<-list
	}
	if _, open := <-list; open {
		t.Error("a listener that fell behind is kept")
	}
}
