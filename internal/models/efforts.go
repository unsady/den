package models

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/unsady/den/internal/config"
)

// Codex's models come from ChatGPT, not from models.dev, so fx's catalogs
// have neither effort levels nor context windows for them. The list fx
// fetches through the proxy has both, and den keeps them from it.

// effortsFile is where den keeps a provider's effort levels by model.
func effortsFile(cfg config.Config, provider string) string {
	return filepath.Join(cfg.State, provider+"-efforts.json")
}

// Efforts returns the effort levels den kept for a provider's model, or
// nil when it has none.
func Efforts(cfg config.Config, provider, model string) []string {
	data, err := os.ReadFile(effortsFile(cfg, provider))
	if err != nil {
		return nil
	}
	var levels map[string][]string
	json.Unmarshal(data, &levels)
	return levels[model]
}

// keepCodexEfforts reads the effort levels from Codex's models list as it
// passes, and leaves the list for fx.
func (m *Models) keepCodexEfforts(resp *http.Response) {
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return
	}

	var list struct {
		Models []struct {
			Slug    string `json:"slug"`
			Context int    `json:"context_window"`
			Levels  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &list) != nil || len(list.Models) == 0 {
		return
	}

	levels := map[string][]string{}
	windows := map[string]int{}
	for _, model := range list.Models {
		for _, l := range model.Levels {
			levels[model.Slug] = append(levels[model.Slug], l.Effort)
		}
		if model.Context > 0 {
			windows[model.Slug] = model.Context
		}
	}
	keep(effortsFile(m.cfg, "codex"), levels)
	keep(windowsFile(m.cfg, "codex"), windows)
}

func keep(path string, v any) {
	data, _ := json.Marshal(v)
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		log.Printf("models: %v", err)
		return
	}
	os.Rename(path+".tmp", path)
}

// windowsFile is where den keeps a provider's context windows by model.
func windowsFile(cfg config.Config, provider string) string {
	return filepath.Join(cfg.State, provider+"-windows.json")
}

// Window returns the context window den kept for a provider's model, in
// tokens, or 0 when it has none.
func Window(cfg config.Config, provider, model string) int {
	data, err := os.ReadFile(windowsFile(cfg, provider))
	if err != nil {
		return 0
	}
	var windows map[string]int
	json.Unmarshal(data, &windows)
	return windows[model]
}
