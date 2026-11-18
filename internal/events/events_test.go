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

	// den's title takes the place of fx's, the name goes beside it, and
	// the listeners hear of both.
	h.Name("s1", "Jensen Huang's apology", "Apology")
	if e := <-l; e.Type != Titled || e.Session != "s1" || e.Title != "Jensen Huang's apology" || e.Name != "Apology" {
		t.Fatalf("event %+v", e)
	}
	list := []sessions.Summary{{ID: "s1", Title: "Summarize https://x.com/..."}, {ID: "s2", Title: "Model speed"}}
	h.Titled(list)
	if list[0].Title != "Jensen Huang's apology" || list[0].Name != "Apology" || list[1].Title != "Model speed" || list[1].Name != "" {
		t.Fatalf("titles %+v", list)
	}

	// An empty name keeps the one before.
	h.Name("s1", "Apology of Jensen Huang", "")
	<-l
	h.Titled(list)
	if list[0].Title != "Apology of Jensen Huang" || list[0].Name != "Apology" {
		t.Fatalf("titles %+v", list)
	}

	h.Forget("s1")
	list[0].Title, list[0].Name = "fx's", ""
	h.Titled(list)
	if list[0].Title != "fx's" || list[0].Name != "" {
		t.Fatalf("forgotten %+v", list[0])
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
