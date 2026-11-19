package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

func newClaude(t *testing.T) (*Claude, string) {
	root := t.TempDir()
	home := filepath.Join(root, "agent", ".fx")
	cfg := config.Config{State: filepath.Join(root, "state"), Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(home, "sessions", "v2")}
	return New(cfg), home
}

func TestSaveRejects(t *testing.T) {
	c, _ := newClaude(t)

	for _, token := range []string{"", "  \n", "sk-ant-api03-abc", "abc"} {
		if err := c.Save(token); err == nil {
			t.Errorf("saved %q", token)
		}
	}
	if c.Connected() {
		t.Error("connected without a token")
	}
}

func TestRoundTrip(t *testing.T) {
	c, home := newClaude(t)

	if _, err := c.Token(); err != ErrNotConnected {
		t.Fatalf("token before save: %v", err)
	}

	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"yolo_acknowledged":true,"providers":{"other":{}},"models":{"other":"x"},"provider":"other"}`), 0o600)

	if err := c.Save(" sk-ant-oat01-abc\n"); err != nil {
		t.Fatal(err)
	}
	token, err := c.Token()
	if err != nil || token != "sk-ant-oat01-abc" || !c.Connected() {
		t.Fatalf("token %q, %v", token, err)
	}
	info, err := os.Stat(filepath.Join(c.cfg.Secrets, File))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode %v, %v", info, err)
	}

	data, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	for _, want := range []string{`"yolo_acknowledged":true`, `"other":"x"`, `"provider":"other"`, `"claude":"sonnet"`, `"base_url":"http://127.0.0.1:4100/claude"`, `"protocol":"openai-chat-completions"`, `"sonnet":{"context_window":200000,"reasoning_efforts":["low","medium","high","xhigh","max"],"supports_tool_use":true,"supports_vision":true}`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings lack %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), "sk-ant") {
		t.Errorf("settings hold the token: %s", data)
	}

	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if c.Connected() {
		t.Error("connected after disconnect")
	}
	data, _ = os.ReadFile(filepath.Join(home, "settings.json"))
	if strings.Contains(string(data), "claude") || !strings.Contains(string(data), `"other":"x"`) {
		t.Errorf("settings after disconnect: %s", data)
	}
	if err := c.Disconnect(); err != nil {
		t.Errorf("second disconnect: %v", err)
	}
}
