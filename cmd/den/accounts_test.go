package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestPickAccount(t *testing.T) {
	for input, want := range map[string]string{
		"\n":        "opencode-go",
		"9\nx\n4\n": "codex",
		"5\n":       "",
	} {
		stdinReader = bufio.NewReader(strings.NewReader(input))
		a, err := pickAccount()
		if err != nil || a.provider != want {
			t.Errorf("%q: got %q, %v; want %q", input, a.provider, err, want)
		}
	}

	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := pickAccount(); err == nil {
		t.Error("no answer: want an error")
	}
}
