package events

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/unsady/den/internal/sessions"
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

	if list, _ := Journal(h.Dir, "job1"); len(list) != 1 || list[0].Title != "morning" {
		t.Fatalf("job journal %+v", list)
	}

	h.Forget("job1")
	if _, err := Journal(h.Dir, "job1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten: %v", err)
	}
}

func TestTitles(t *testing.T) {
	h := &Hub{Dir: t.TempDir()}
	l, stop := h.Listen()
	defer stop()

	// den's title takes the place of fx's, and the listeners hear of it.
	h.Name("s1", "Jensen Huang's apology")
	if e := <-l; e.Type != Titled || e.Session != "s1" || e.Text != "Jensen Huang's apology" {
		t.Fatalf("event %+v", e)
	}
	list := []sessions.Summary{{ID: "s1", Title: "Summarize https://x.com/..."}, {ID: "s2", Title: "Model speed"}}
	h.Titled(list)
	if list[0].Title != "Jensen Huang's apology" || list[1].Title != "Model speed" {
		t.Fatalf("titles %+v", list)
	}

	h.Forget("s1")
	list[0].Title = "fx's"
	h.Titled(list)
	if list[0].Title != "fx's" {
		t.Fatalf("forgotten title %q", list[0].Title)
	}
}

func TestFile(t *testing.T) {
	h := &Hub{Dir: t.TempDir()}

	if err := h.SaveFile("s1", "/tmp/Chart.PNG", "Speed", []byte("png")); err != nil {
		t.Fatal(err)
	}

	list, err := Journal(h.Dir, "s1")
	if err != nil || len(list) != 1 || list[0].Type != File || list[0].Text != "Speed" || !strings.HasSuffix(list[0].File, ".png") {
		t.Fatalf("journal %+v, %v", list, err)
	}
	if data, err := os.ReadFile(h.FilePath("s1", list[0].File)); err != nil || string(data) != "png" {
		t.Fatalf("file %q, %v", data, err)
	}

	h.Forget("s1")
	if _, err := os.Stat(h.FilePath("s1", list[0].File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forgotten: %v", err)
	}
}
