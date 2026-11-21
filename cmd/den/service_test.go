package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/unsady/den/internal/fetch"
)

func TestAskService(t *testing.T) {
	saved := stdinReader
	t.Cleanup(func() { stdinReader = saved })
	answers := func(lines ...string) {
		stdinReader = bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	}

	answers("todo", "https://api.todoist.com/rest/v2", "", "", "Tasks API; docs at https://developer.todoist.com")
	s, err := askService()
	if err != nil {
		t.Fatal(err)
	}
	want := fetch.Custom{Name: "todo", Host: "api.todoist.com", Ask: fetch.AskWrites, About: "Tasks API; docs at https://developer.todoist.com"}
	if s != want {
		t.Errorf("got %+v, want %+v", s, want)
	}

	answers("crm", "https://crm.example.com", "X-Api-Key", "3", "")
	s, err = askService()
	if err != nil {
		t.Fatal(err)
	}
	if s.Header != "X-Api-Key" || s.Ask != fetch.AskAlways {
		t.Errorf("got %+v, want the header X-Api-Key and ask always", s)
	}

	answers("Bad Name")
	if _, err := askService(); err == nil {
		t.Error("a name with spaces passed")
	}
	answers("crm", "http://crm.example.com")
	if _, err := askService(); err == nil {
		t.Error("an http address passed")
	}
}
