package models

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

func TestKeepCodexEfforts(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	m := &Models{cfg: cfg}

	list := `{"models":[{"slug":"gpt-terra","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},{"slug":"gpt-mini","supported_reasoning_levels":[]}]}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(list))}
	m.keepCodexEfforts(resp)

	// fx still gets the whole list.
	if body, _ := io.ReadAll(resp.Body); string(body) != list {
		t.Fatalf("body changed: %s", body)
	}
	if got := Efforts(cfg, "codex", "gpt-terra"); !slices.Equal(got, []string{"low", "high"}) {
		t.Errorf("gpt-terra: %v", got)
	}
	if got := Efforts(cfg, "codex", "gpt-mini"); got != nil {
		t.Errorf("gpt-mini has no levels, got %v", got)
	}
	if got := Efforts(cfg, "opencode", "gpt-terra"); got != nil {
		t.Errorf("another provider has none kept, got %v", got)
	}
}
