package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMapProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{
		"provider": "codex",
		"models": {"codex": "sol", "opencode-go": "deepseek", "zen": "kimi"},
		"providers": {
			"opencode-go": {"base_url": "http://127.0.0.1:4100/opencode-go", "protocol": "openai-chat-completions"},
			"zen": {"base_url": "http://127.0.0.1:4100/zen/", "protocol": "openai-chat-completions"},
			"anthropic": {"base_url": "http://127.0.0.1:4100/anthropic", "protocol": "anthropic-messages"}
		}
	}`), 0o600)

	for model, want := range map[string]string{
		"":                   "opencode-go deepseek http://127.0.0.1:4100/opencode-go/chat/completions",
		"haiku":              "opencode-go haiku http://127.0.0.1:4100/opencode-go/chat/completions",
		"zen/glm":            "zen glm http://127.0.0.1:4100/zen/chat/completions",
		"zen/":               "zen kimi http://127.0.0.1:4100/zen/chat/completions",
		"moonshotai/kimi-k3": "opencode-go moonshotai/kimi-k3 http://127.0.0.1:4100/opencode-go/chat/completions",
	} {
		got, err := mapProvider(path, model)
		if err != nil {
			t.Errorf("%q: %v", model, err)
			continue
		}
		if s := got.name + " " + got.model + " " + got.url; s != want {
			t.Errorf("%q: got %s, want %s", model, s, want)
		}
	}

	if _, err := mapProvider(path, "anthropic/haiku"); err == nil || !strings.Contains(err.Error(), "opencode-go, zen") {
		t.Errorf("anthropic: %v", err)
	}
}
