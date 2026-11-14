// Package claude connects den to the user's Claude Pro or Max subscription
// and keeps its token away from the agent.
//
// The user makes the token with `claude setup-token` and hands it to den,
// which keeps it in its secrets. The token lasts a year, so den never
// refreshes it. den passes it only to the claude command it runs itself,
// and fx, which the agent runs, reaches Claude through den's model proxy.
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
)

const (
	// File is where den keeps the account's token.
	File = "claude"

	// Prefix starts every token that `claude setup-token` makes.
	Prefix = "sk-ant-oat01-"

	// Provider is the id of Claude in fx's settings.
	Provider = "claude"

	// Model is the one fx starts with; /model in Telegram changes it.
	Model = "sonnet"

	// origin is den's model proxy.
	origin = "http://127.0.0.1:4100"

	// address is where fx reaches Claude, through den's model proxy.
	address = origin + "/" + Provider
)

// Models are the aliases the claude command takes, each for the newest
// model of its kind. fx lists a provider's models from what its settings
// declare, and sizes its compaction by the context window.
var Models = []string{"opus", "sonnet", "haiku"}

// Efforts are the levels `claude --effort` takes.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// window is the context window den assumes until claude reports a model's:
// with a smaller one fx only compacts early, with a larger one it fails.
const window = 200000

// Window is model's context window as claude last reported it.
func Window(cfg config.Config, model string) int {
	var windows map[string]int
	if data, err := os.ReadFile(windowsFile(cfg)); err == nil {
		json.Unmarshal(data, &windows)
	}
	if w := windows[model]; w > 0 {
		return w
	}
	return window
}

func windowsFile(cfg config.Config) string {
	return filepath.Join(cfg.State, "claude", "windows.json")
}

// learn keeps the context window claude reported for model, and tells fx,
// which compacts by it.
func (c *Claude) learn(model string, size int) {
	if size <= 0 || Window(c.cfg, model) == size {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	windows := map[string]int{}
	if data, err := os.ReadFile(windowsFile(c.cfg)); err == nil {
		json.Unmarshal(data, &windows)
	}
	windows[model] = size
	data, _ := json.Marshal(windows)
	path := windowsFile(c.cfg)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		log.Printf("claude: %v", err)
		return
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		log.Printf("claude: %v", err)
		return
	}
	if err := c.Register(); err != nil {
		log.Printf("claude: %v", err)
	}
}

// ErrNotConnected means there is no token.
var ErrNotConnected = errors.New("Claude is not connected: the user connects it with `den connect claude` on their computer")

type Claude struct {
	cfg config.Config
	// home is the agent's fx profile, ~/.fx.
	home string
	// bin replaces the downloaded claude in tests.
	bin string
	// base is den's proxy as claude reaches it; tests change it.
	base string
	// bridge holds the claude processes that answer fx.
	bridge bridge

	mu sync.Mutex
}

func New(cfg config.Config) *Claude {
	c := &Claude{cfg: cfg, home: filepath.Dir(filepath.Dir(cfg.Sessions)), base: origin}
	c.bridge.max = cfg.ClaudeProcesses
	return c
}

// Save keeps the token and gives fx a Claude provider.
func (c *Claude) Save(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("the token is empty")
	}
	if !strings.HasPrefix(token, Prefix) {
		return fmt.Errorf("this is not a token from `claude setup-token`: it starts with %s", Prefix)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.MkdirAll(c.cfg.Secrets, 0o700); err != nil {
		return err
	}

	path := c.path()
	if err := os.WriteFile(path+".tmp", []byte(token), 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	return c.Register()
}

// Token returns the token.
func (c *Claude) Token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path())
	token := strings.TrimSpace(string(data))
	if err != nil || token == "" {
		return "", ErrNotConnected
	}
	return token, nil
}

// Connected reports whether a token is kept.
func (c *Claude) Connected() bool {
	_, err := c.Token()
	return err == nil
}

// Disconnect forgets the token and fx's Claude provider.
func (c *Claude) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.Remove(c.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return c.edit(func(settings map[string]json.RawMessage) {
		providers := map[string]json.RawMessage{}
		json.Unmarshal(settings["providers"], &providers)
		delete(providers, Provider)
		settings["providers"], _ = json.Marshal(providers)

		models := map[string]string{}
		json.Unmarshal(settings["models"], &models)
		delete(models, Provider)
		settings["models"], _ = json.Marshal(models)

		var current string
		json.Unmarshal(settings["provider"], &current)
		if current == Provider {
			delete(settings, "provider")
		}
	})
}

// Register points fx at den's proxy for Claude, as models.Login does for a
// provider with a key, but without a key: the proxy needs none from fx.
// The daemon runs it again on start, so an older entry gets new models.
func (c *Claude) Register() error {
	return c.edit(func(settings map[string]json.RawMessage) {
		providers := map[string]json.RawMessage{}
		json.Unmarshal(settings["providers"], &providers)
		models := map[string]any{}
		for _, id := range Models {
			models[id] = map[string]any{"context_window": Window(c.cfg, id), "supports_tool_use": true, "reasoning_efforts": Efforts}
		}
		providers[Provider], _ = json.Marshal(map[string]any{
			"protocol":       "openai-chat-completions",
			"base_url":       address,
			"auth":           map[string]string{"type": "none"},
			"model_metadata": models,
		})
		settings["providers"], _ = json.Marshal(providers)

		// fx takes no provider without a model of its own.
		saved := map[string]string{}
		json.Unmarshal(settings["models"], &saved)
		if saved[Provider] == "" {
			saved[Provider] = Model
		}
		settings["models"], _ = json.Marshal(saved)

		// The first provider den gets is the one it uses.
		if _, ok := settings["provider"]; !ok {
			settings["provider"], _ = json.Marshal(Provider)
		}
	})
}

// edit changes fx's settings, which belong to the agent.
func (c *Claude) edit(change func(map[string]json.RawMessage)) error {
	path := filepath.Join(c.home, "settings.json")
	settings := map[string]json.RawMessage{}
	if data, err := proc.ReadFile(c.cfg, path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
	}
	change(settings)

	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return proc.WriteFile(c.cfg, path, out, 0o600)
}

func (c *Claude) path() string { return filepath.Join(c.cfg.Secrets, File) }
