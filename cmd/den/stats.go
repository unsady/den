package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/models"
)

// statsCommand prints how each model provider answered, from den's own
// measurements in the model proxy.
func statsCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	since := fs.Duration("since", 24*time.Hour, "how far back to look")
	asJSON := fs.Bool("json", false, "print every call as JSON")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New("stats takes only --since and --json")
	}

	var calls []models.Call
	path := "/stats?since=" + url.QueryEscape(since.String())
	if err := client.New(cfg).Do(http.MethodGet, path, nil, &calls); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, c := range calls {
			enc.Encode(c)
		}
		return nil
	}
	if len(calls) == 0 {
		fmt.Printf("No model calls in the last %s.\n", *since)
		return nil
	}
	fmt.Print(summary(calls))
	return nil
}

// summary says per provider and model how many calls failed, how long the
// provider was silent before the first token, the longest stall, and the
// writing speed.
func summary(calls []models.Call) string {
	type group struct {
		key            string
		calls, failed  int
		firsts, speeds []float64
		gap            int
		lastError      string
	}
	var groups []*group
	find := func(key string) *group {
		for _, g := range groups {
			if g.key == key {
				return g
			}
		}
		g := &group{key: key}
		groups = append(groups, g)
		return g
	}
	for _, c := range calls {
		g := find(c.Provider + " · " + c.Model)
		g.calls++
		if c.Status >= 400 || c.Error != "" {
			g.failed++
			g.lastError = fmt.Sprintf("%d %s", c.Status, clip(c.Error, 100))
			continue
		}
		if c.First > 0 {
			g.firsts = append(g.firsts, float64(c.First)/1000)
			if writing := c.Total - c.First; writing > 0 && c.Out > 0 {
				g.speeds = append(g.speeds, float64(c.Out)*1000/float64(writing))
			}
		}
		g.gap = max(g.gap, c.Gap)
	}

	text := fmt.Sprintf("%d calls since %s\n", len(calls), calls[0].At.Local().Format("Jan 2 15:04"))
	for _, g := range groups {
		text += fmt.Sprintf("\n%s: %d calls", g.key, g.calls)
		if g.failed > 0 {
			text += fmt.Sprintf(", %d failed (last: %s)", g.failed, g.lastError)
		}
		if len(g.firsts) > 0 {
			text += fmt.Sprintf("\n  first token: median %.1fs, p90 %.1fs, max %.1fs", quantile(g.firsts, 0.5), quantile(g.firsts, 0.9), quantile(g.firsts, 1))
			text += fmt.Sprintf("\n  longest stall after it: %.1fs", float64(g.gap)/1000)
		}
		if len(g.speeds) > 0 {
			text += fmt.Sprintf("\n  writing speed: median %.0f tok/s, p10 %.0f", quantile(g.speeds, 0.5), quantile(g.speeds, 0.1))
		}
		text += "\n"
	}
	return text
}

func quantile(values []float64, q float64) float64 {
	sorted := slices.Sorted(slices.Values(values))
	return sorted[min(len(sorted)-1, int(q*float64(len(sorted)-1)+0.5))]
}

func clip(text string, n int) string {
	if r := []rune(text); len(r) > n {
		return string(r[:n]) + "…"
	}
	return text
}
