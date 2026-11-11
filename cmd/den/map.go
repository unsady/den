package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/mapper"
	"github.com/unsady/den/internal/models"
)

// mapCommand asks a model about each line of a JSONL file in its own short
// request.
func mapCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	prompt := fs.String("p", "", "the instructions for every item, or @file to read them")
	model := fs.String("model", "", "provider/model, or a model of fx's provider; fx's model when empty")
	out := fs.String("out", "", "where to write the answers; <items>.out.jsonl when empty")
	workers := fs.Int("workers", 6, "how many items to ask about at once")
	resume := fs.Bool("resume", false, "keep the answers already in the output file and ask only for the rest")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || *prompt == "" {
		return errors.New("map needs -p with the instructions and one JSONL file of items")
	}

	instructions := *prompt
	if path, ok := strings.CutPrefix(instructions, "@"); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		instructions = string(data)
	}

	provider, err := mapProvider(filepath.Join(cfg.Sessions, "..", "..", "settings.json"), *model)
	if err != nil {
		return err
	}

	in, err := os.Open(rest[0])
	if err != nil {
		return err
	}
	defer in.Close()
	if *out == "" {
		*out = strings.TrimSuffix(rest[0], ".jsonl") + ".out.jsonl"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	opts := mapper.Options{
		URL:          provider.url,
		Model:        provider.model,
		Instructions: instructions,
		Workers:      *workers,
		Resume:       *resume,
		Wait:         5 * time.Second,
		Progress:     os.Stderr,
		Every:        15 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "Asking %s/%s about each item of %s…\n", provider.name, provider.model, rest[0])
	sum, err := mapper.Run(ctx, opts, in, *out)
	printMapSummary(os.Stdout, *out, provider, sum)
	return err
}

type mapTarget struct {
	name, model, url string
}

// mapProvider picks the provider and model from fx's settings: those den
// proxies with the chat completions API, as subscriptions such as Codex
// speak another one.
func mapProvider(settingsPath, model string) (mapTarget, error) {
	var settings struct {
		Provider  string            `json:"provider"`
		Models    map[string]string `json:"models"`
		Providers map[string]struct {
			BaseURL  string `json:"base_url"`
			Protocol string `json:"protocol"`
		} `json:"providers"`
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return mapTarget{}, fmt.Errorf("reading fx's settings: %v", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return mapTarget{}, fmt.Errorf("%s: %v", settingsPath, err)
	}

	var usable []string
	for name, p := range settings.Providers {
		if p.Protocol == "openai-chat-completions" && strings.HasPrefix(p.BaseURL, "http://"+models.Proxy+"/") {
			usable = append(usable, name)
		}
	}
	slices.Sort(usable)
	if len(usable) == 0 {
		return mapTarget{}, errors.New("den map needs a provider signed in with den login, such as opencode-go; Codex is not supported")
	}

	name := settings.Provider
	p, m, named := strings.Cut(model, "/")
	named = named && settings.Providers[p].BaseURL != ""
	if named {
		name, model = p, m
	}
	if !slices.Contains(usable, name) {
		if named {
			return mapTarget{}, fmt.Errorf("den map works with %s, not %s", strings.Join(usable, ", "), name)
		}
		// fx's own provider may be a subscription den cannot map with.
		name = usable[0]
	}
	if model == "" {
		model = settings.Models[name]
	}
	if model == "" {
		return mapTarget{}, fmt.Errorf("fx has no model for %s; pass --model %s/<model>", name, name)
	}
	return mapTarget{name, model, strings.TrimSuffix(settings.Providers[name].BaseURL, "/") + "/chat/completions"}, nil
}

func printMapSummary(w io.Writer, out string, t mapTarget, sum mapper.Summary) {
	fmt.Fprintf(w, "%s/%s answered %d of %d items in %.1f min", t.name, t.model, sum.Answered, sum.Items, sum.Minutes)
	if sum.Kept > 0 {
		fmt.Fprintf(w, " (%d kept from before)", sum.Kept)
	}
	fmt.Fprintf(w, "; %d failed.\n", sum.Failed)
	fmt.Fprintf(w, "Tokens: %d in (%d cached), %d out", sum.Input, sum.Cached, sum.Output)
	if sum.Cost > 0 {
		fmt.Fprintf(w, "; about $%.4f at list price", sum.Cost)
	}
	fmt.Fprintln(w, ".")

	keys := make([]string, 0, len(sum.Values))
	for k := range sum.Values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		counts := sum.Values[k]
		vals := make([]string, 0, len(counts))
		for v := range counts {
			vals = append(vals, v)
		}
		slices.SortFunc(vals, func(a, b string) int { return counts[b] - counts[a] })
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = fmt.Sprintf("%s %d", v, counts[v])
		}
		fmt.Fprintf(w, "%s: %s\n", k, strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "Answers: %s\n", out)
}
