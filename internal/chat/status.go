package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/models"
	"github.com/unsady/den/internal/runner"
)

// status says in a few lines what the chat runs on: the model, how full
// its context is, the container's memory, and the jobs.
func (c *Chat) status(ctx context.Context) string {
	c.mu.Lock()
	provider, model, effort, used, session := c.state.Provider, c.state.Model, c.state.Effort, c.state.Context, c.state.Session
	c.mu.Unlock()

	var lines []string
	setup, err := runner.Status(ctx, c.cfg, provider)
	if err == nil {
		if model == "" {
			model = setup.Model
		}
		lines = append(lines, "⚙︎ "+boldModel(model, setup)+effortName(effort))
		if second := c.second(); second != nil {
			lines = append(lines, "Second: "+secondName(*second, setup))
		}
	}

	window := c.window(setup.Provider, model)
	switch {
	case session == "":
		lines = append(lines, "Context: new session")
	case used > 0 && window > 0:
		lines = append(lines, fmt.Sprintf("Context: %s of %s (%d%%)", tokens(used), tokens(window), used*100/window))
	case used > 0:
		lines = append(lines, "Context: "+tokens(used))
	default:
		lines = append(lines, "Context: known after the next reply")
	}

	if inUse, total := memory(); total > 0 {
		lines = append(lines, fmt.Sprintf("Memory: %s of %s", size(inUse), size(total)))
	}

	var list []jobs.Job
	if c.den != nil {
		list = c.den.ListJobs()
	}
	return strings.Join(append(lines, jobsLines(list)...), "\n")
}

// jobsLines sum the jobs up in a line, with the next one to run, and name
// only those whose last run failed.
func jobsLines(list []jobs.Job) []string {
	if len(list) == 0 {
		return nil
	}
	var lines []string
	var next *jobs.Job
	for i, j := range list {
		if j.LastRun != nil && j.LastStatus != "ok" {
			lines = append(lines, fmt.Sprintf("✗ %s failed %s", html.EscapeString(j.Label()), j.LastRun.Local().Format("Jan 2 15:04")))
		}
		if !j.NextRun.IsZero() && (next == nil || j.NextRun.Before(next.NextRun)) {
			next = &list[i]
		}
	}

	summary := fmt.Sprintf("Jobs: %d", len(list))
	if next != nil {
		summary += fmt.Sprintf(" · next %s at %s", html.EscapeString(next.Label()), next.NextRun.Local().Format("15:04"))
	}
	return append([]string{summary}, lines...)
}

// window reads the model's context window from fx's catalog of the
// provider, or 0 when it does not say.
func (c *Chat) window(provider, model string) int {
	// The sessions live at ~/.fx/sessions/v2, the catalogs beside them.
	home := filepath.Dir(filepath.Dir(c.cfg.Sessions))
	data, err := os.ReadFile(filepath.Join(home, "model-catalogs", provider+".json"))
	var catalog map[string]struct {
		Limit struct {
			Context int `json:"context"`
		} `json:"limit"`
	}
	if err == nil {
		json.Unmarshal(data, &catalog)
	}
	if window := catalog[model].Limit.Context; window > 0 {
		return window
	}
	return models.Window(c.cfg, provider, model)
}

// efforts reads the model's reasoning effort levels from fx's catalog of
// the provider, or from the ones den keeps for Codex, none when neither
// says.
func (c *Chat) efforts(provider, model string) []string {
	home := filepath.Dir(filepath.Dir(c.cfg.Sessions))
	data, err := os.ReadFile(filepath.Join(home, "model-catalogs", provider+".json"))
	if err != nil {
		return models.Efforts(c.cfg, provider, model)
	}
	var catalog map[string]struct {
		Options []struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		} `json:"reasoning_options"`
	}
	if json.Unmarshal(data, &catalog) != nil {
		return nil
	}
	for _, o := range catalog[model].Options {
		if o.Type == "effort" {
			return o.Values
		}
	}
	return nil
}

// tokens writes a count of tokens briefly, such as 38k or 1.2M.
func tokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

// bytes writes a size in MB, or GB from 1 GB.
func size(n int64) string {
	if n >= 1<<30 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<30)), ".0") + " GB"
	}
	return fmt.Sprintf("%d MB", n>>20)
}

// memory reads the machine's used and total memory. The container is its
// own virtual machine, so that is the container's.
func memory() (used, total int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	var available int64
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var key string
		var kb int64
		if _, err := fmt.Sscan(scan.Text(), &key, &kb); err != nil {
			continue
		}
		switch key {
		case "MemTotal:":
			total = kb << 10
		case "MemAvailable:":
			available = kb << 10
		}
	}
	return total - available, total
}

// contextSize is the largest prompt since started, or 0 without a daemon.
func (c *Chat) contextSize(started time.Time) int {
	if c.den == nil {
		return 0
	}
	return c.den.ContextSize(started)
}
