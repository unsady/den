package events

import (
	"errors"
	"os"
	"strings"
	"testing"
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
