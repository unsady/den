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
			"claude": {"base_url": "http://127.0.0.1:4100/claude", "protocol": "openai-chat-completions"},
			"anthropic": {"base_url": "http://127.0.0.1:4100/anthropic", "protocol": "anthropic-messages"}
		}
	}`), 0o600)

	for model, want := range map[string]string{
		"":                   "opencode-go deepseek",
		"haiku":              "opencode-go haiku",
		"zen/glm":            "zen glm",
		"zen/":               "zen kimi",
		"moonshotai/kimi-k3": "opencode-go moonshotai/kimi-k3",
	} {
		got, err := mapProvider(path, model)
		if err != nil {
			t.Errorf("%q: %v", model, err)
			continue
		}
		if s := got.name + " " + got.model; s != want {
			t.Errorf("%q: got %s, want %s", model, s, want)
		}
	}

	// A subscription serves when named with its model.
	for _, sub := range []string{"codex/gpt-6-luna", "grok/grok-4.5", "claude/haiku"} {
		name, model, _ := strings.Cut(sub, "/")
		got, err := mapProvider(path, sub)
		if err != nil || got.name != name || got.model != model {
			t.Errorf("%s: %+v, %v", sub, got, err)
		}
		if _, err := mapProvider(path, name+"/"); err == nil {
			t.Errorf("%s without a model was taken", name)
		}
	}

	if _, err := mapProvider(path, "anthropic/haiku"); err == nil || !strings.Contains(err.Error(), "opencode-go, zen") {
		t.Errorf("anthropic: %v", err)
	}
}
