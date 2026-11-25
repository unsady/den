package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/unsady/den/internal/acp"
)

func TestThoughtLine(t *testing.T) {
	long := strings.Repeat("a", thoughtWidth+40)
	cases := []struct{ in, want string }{
		{"Comparing the two", "Comparing the two"},
		{"**Planning the trip**\n\nComparing the two flights", "Comparing the two flights"},
		{"**Planning the trip**\n\n", "Planning the trip"},
		{"## Heading\n", "Heading"},
		{"  \n ", ""},
		{"first\n" + long, "…" + long[40:]},
	}
	for _, c := range cases {
		if got := thoughtLine(c.in); got != c.want {
			t.Errorf("thoughtLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStepLine(t *testing.T) {
	cases := []struct{ raw, title, want string }{
		{`{"rawInput":{"command":"ls -la"}}`, "Running", "Running ls -la"},
		{`{"rawInput":{"path":"notes/todo.md"}}`, "Reading", "Reading notes/todo.md"},
		{`{"rawInput":{"url":"https://example.com"}}`, "Fetching", "Fetching https://example.com"},
		{`{"rawInput":{"query":"flights"}}`, "Searching", "Searching flights"},
		{`{}`, "Thinking", "Thinking"},
	}
	for _, c := range cases {
		ev := acp.Event{Title: c.title, Raw: json.RawMessage(c.raw)}
		if got := stepLine(ev); got != c.want {
			t.Errorf("stepLine(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestTail(t *testing.T) {
	tl := &tail{limit: 8}
	tl.Write([]byte("hello "))
	tl.Write([]byte("world"))
	if got := tl.String(); got != "lo world" {
		t.Errorf("tail = %q", got)
	}
	if got := lastLine("one\ntwo\n\n"); got != "two" {
		t.Errorf("lastLine = %q", got)
	}
}
