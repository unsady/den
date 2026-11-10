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
// have no effort levels for them. The list fx fetches through the proxy has
// them, and den keeps them from it.

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
			Slug   string `json:"slug"`
			Levels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &list) != nil || len(list.Models) == 0 {
		return
	}

	levels := map[string][]string{}
	for _, model := range list.Models {
		for _, l := range model.Levels {
			levels[model.Slug] = append(levels[model.Slug], l.Effort)
		}
	}
	data, _ := json.Marshal(levels)
	path := effortsFile(m.cfg, "codex")
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		log.Printf("models: %v", err)
		return
	}
	os.Rename(path+".tmp", path)
}
