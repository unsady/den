package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestChooseLine(t *testing.T) {
	choices := []choice{{"OpenCode Go", "", "opencode-go"}, {"OpenRouter", "", "openrouter"}, {"DeepSeek", "", "deepseek"}}
	for input, want := range map[string]string{
		"\n":            "openrouter",
		"9\nx\n3\n":     "deepseek",
		"OpenCode Go\n": "opencode-go",
		"deepseek\n":    "deepseek",
	} {
		stdinReader = bufio.NewReader(strings.NewReader(input))
		got, err := chooseLine("Account", choices, "openrouter")
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", input, got, err, want)
		}
	}

	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := chooseLine("Account", choices, ""); err == nil {
		t.Error("no answer: want an error")
	}
}
