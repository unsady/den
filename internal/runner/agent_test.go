package runner

import (
	"encoding/json"
	"testing"

	"github.com/unsady/den/internal/acp"
)

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
