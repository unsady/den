package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/daemon"
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
	workers := fs.Int("workers", 4, fmt.Sprintf("how many items to ask about at once, up to %d", maxWorkers))
	effort := fs.String("effort", "", "the model's reasoning effort, such as none or low; the provider's default when empty, low for Codex")
	resume := fs.Bool("resume", false, "keep the answers already in the output file and ask only for the rest")
	run := fs.String("run", "", "set by den when it runs a run the user allowed")
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

	if *workers > maxWorkers {
		fmt.Fprintf(os.Stderr, "den map runs at most %d items at once.\n", maxWorkers)
		*workers = maxWorkers
	}

	data, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	if *out == "" {
		*out = strings.TrimSuffix(rest[0], ".jsonl") + ".out.jsonl"
	}

	// Every run spends the user's subscription or money, so the user
	// allows it first, or allowed this model for good. When den has to ask,
	// the command does not wait: den runs it again once the user allows it,
	// and tells the agent what came of it.
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	ask := daemon.MapRequest{
		Provider:     provider.name,
		Model:        provider.model,
		Effort:       *effort,
		File:         rest[0],
		Instructions: instructions,
		Args:         args,
		Dir:          dir,
		Turn:         os.Getenv("DEN_TURN"),
		Run:          *run,
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			ask.Items++
		}
	}
	var answer daemon.MapReply
	if err := client.New(cfg).Wait(2*time.Minute).Do(http.MethodPost, "/map", ask, &answer); err != nil {
		return fmt.Errorf("not run: %w", err)
	}
	if answer.Waiting {
		fmt.Println("den asked the user to allow this run in Telegram. Once they answer, den runs it and tells you what came of it in a new message, so you need not wait: go on, or end your turn.")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	opts := mapper.Options{
		URL:          provider.url,
		Responses:    provider.name == "codex",
		Model:        provider.model,
		Effort:       *effort,
		Instructions: instructions,
		Workers:      *workers,
		Resume:       *resume,
		Wait:         5 * time.Second,
		Progress:     mapProgress(cfg),
		Every:        10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "Asking %s/%s about each item of %s…\n", provider.name, provider.model, rest[0])
	sum, err := mapper.Run(ctx, opts, bytes.NewReader(data), *out)
	printMapSummary(os.Stdout, *out, provider, sum)
	return err
}

// mapProgress prints how far a run is for the agent, and has den show it
// under the step in the user's chat.
func mapProgress(cfg config.Config) func(mapper.Status) {
	return func(s mapper.Status) {
		fmt.Fprintf(os.Stderr, "den map: %d of %d done, %d failed, $%.4f so far\n", s.Done, s.Total, s.Failed, s.Cost)
		text := fmt.Sprintf("🗂 %d of %d items", s.Done, s.Total)
		if s.Failed > 0 {
			text += fmt.Sprintf(", %d failed", s.Failed)
		}
		chatNote(cfg, text)
	}
}

// chatNote shows text under the running step in the user's chat, when the
// chat's agent runs this command.
func chatNote(cfg config.Config, text string) {
	turn := os.Getenv("DEN_TURN")
	if turn == "" {
		return
	}
	// The chat is a nicety: a daemon that is away does not stop the run.
	_ = client.New(cfg).Wait(5*time.Second).Do(http.MethodPost, "/progress", daemon.ProgressRequest{Turn: turn, Text: text}, nil)
}

// maxWorkers bounds how many requests one run sends at once, so a provider
// does not refuse them as too many.
const maxWorkers = 8

type mapTarget struct {
	name, model, url string
}

// mapProvider picks the provider and model from fx's settings: those den
// proxies with the chat completions API. A ChatGPT subscription's Codex
// serves only when named with its model, so a run does not spend the
// subscription on its strongest model by default.
func mapProvider(settingsPath, model string) (mapTarget, error) {
	if m, ok := strings.CutPrefix(model, "codex/"); ok {
		if m == "" {
			return mapTarget{}, errors.New("name the Codex model, such as --model codex/gpt-6-luna; `fx models` lists them")
		}
		return mapTarget{"codex", m, "http://" + models.Proxy + "/codex/responses"}, nil
	}

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
		return mapTarget{}, errors.New("den map needs a provider signed in with den login, such as opencode-go, or a Codex model named with --model codex/<model>, such as codex/gpt-6-luna")
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
