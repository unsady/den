// Package runner runs noninteractive fx requests for jobs and chat.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/sessions"
)

// Silent is the reply that tells den a job has nothing to deliver.
const Silent = "SILENT"

// Yourself asks the agent to work without subagents where it can: each
// one spends the same model's limits, and its report can come after the
// answer.
const Yourself = "Do the work yourself; start a subagent only for a long part that is independent of the rest."

const deliveredNote = "This is the scheduled den job %q (id %s). It runs in a fresh session without the user's chat history. " +
	"Your final reply is sent to the user as a notification, so keep it short and self-contained. " +
	"Reply with exactly " + Silent + " when there is nothing worth sending. " + Yourself

const silentNote = "This is the scheduled den job %q (id %s). It runs in a fresh session without the user's chat history. " +
	"Your final reply is saved in the job log and is not sent to the user. " + Yourself

// Prompt is the request fx receives for j. The job's own words come first,
// because fx names a session after the start of its first message when the
// provider has no model for titles.
func Prompt(j jobs.Job) string {
	note := deliveredNote
	if j.Silent {
		note = silentNote
	}
	return j.Prompt + "\n\n[" + fmt.Sprintf(note, j.Label(), j.ID) + "]"
}

// Choice is a model to use with its reasoning effort. Empty fields leave
// fx's own choice.
type Choice struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// Run runs j on the model choice names.
func Run(ctx context.Context, cfg config.Config, j jobs.Job, choice Choice) jobs.Run {
	reply := Ask(ctx, cfg, Request{Prompt: Prompt(j), Provider: choice.Provider, Model: choice.Model, Effort: choice.Effort})
	return jobs.Run{
		JobID:     j.ID,
		Started:   reply.Started,
		Duration:  reply.Duration,
		ExitCode:  reply.ExitCode,
		SessionID: reply.SessionID,
		Output:    reply.Output,
		Error:     reply.Error,
		Stderr:    reply.Stderr,
		Model:     choice.Model,

		InputTokens:  reply.InputTokens,
		OutputTokens: reply.OutputTokens,
	}
}

// Request is one fx ask call.
type Request struct {
	Prompt string
	// Provider and Model, when set, override fx's own choice for this request.
	Provider string
	Model    string
	// Effort, when set, is the model's reasoning effort, such as high.
	Effort string
	// Resume continues this session id instead of starting a new session.
	Resume string
	// Images are attached to the request, as fx ask --image does.
	Images []string
	// CompactPercent, when set, makes fx compact the session before the
	// request once it fills this share of the model's window.
	CompactPercent int
	// Progress, when set, receives each line fx writes to stderr while it
	// works, such as "Reading notes/todo.md" before a tool runs.
	Progress func(line string)
}

// Reply is what one fx ask call produced. Error is empty on success.
type Reply struct {
	Started   time.Time
	Duration  time.Duration
	ExitCode  int
	SessionID string
	Output    string
	Error     string
	Stderr    string
	// Input and Output tokens are what fx reported, 0 when it reported none.
	InputTokens  int64
	OutputTokens int64
}

// Ask runs fx ask as the agent in the space, bounded by cfg.JobTimeout.
// Cancelling ctx stops fx and everything it started.
func Ask(ctx context.Context, cfg config.Config, req Request) (reply Reply) {
	reply = Reply{Started: time.Now()}
	defer func() { reply.Duration = time.Since(reply.Started).Round(time.Second) }()

	ctx, cancel := context.WithTimeout(ctx, cfg.JobTimeout)
	defer cancel()

	args := []string{"ask", "--full-access", "--json", "--no-color"}
	if req.Provider != "" {
		args = append(args, "--provider", req.Provider)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.Resume != "" {
		args = append(args, "--resume", req.Resume)
	}
	for _, image := range req.Images {
		args = append(args, "--image", image)
	}
	args = append(args, "--", req.Prompt)

	cmd, err := proc.Command(ctx, cfg, cfg.FX, args...)
	if err != nil {
		reply.Error = err.Error()
		return reply
	}
	cmd.Dir = cfg.Space
	if req.CompactPercent > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("FX_AUTO_COMPACT_PERCENT=%d", req.CompactPercent))
	}

	var stdout bytes.Buffer
	stderr := &tail{limit: 8 << 10}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if req.Progress != nil {
		cmd.Stderr = io.MultiWriter(stderr, &lines{emit: req.Progress})
	}

	ooms := proc.OOMKills()
	err = cmd.Run()
	reply.Stderr = stderr.String()

	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		reply.ExitCode = exit.ExitCode()
	case err != nil:
		reply.ExitCode = -1
	}

	result, parseErr := parse(stdout.Bytes())
	reply.Output = result.output()
	reply.SessionID = result.SessionID
	if reply.Output == Silent || reply.Output == "Done." {
		// A subagent's report came after the answer and took the last word;
		// the answer is in the session.
		if answer := sessions.Answer(cfg.Sessions, reply.SessionID, reply.Started); answer != "" {
			reply.Output = answer
		}
	}
	if result.Usage != nil {
		reply.InputTokens = value(result.Usage.Input)
		reply.OutputTokens = value(result.Usage.Output)
	}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		reply.Error = fmt.Sprintf("timed out after %s", cfg.JobTimeout)
	case ctx.Err() == context.Canceled:
		reply.Error = "stopped"
	case proc.OutOfMemory(err, ooms):
		reply.Error = fmt.Sprintf("fx ran out of memory: the kernel stopped it, and the container has %s", proc.MemoryLimit())
	case err != nil:
		reply.Error = fmt.Sprintf("fx exited: %v", err)
	case parseErr != nil:
		reply.Error = parseErr.Error()
	}
	return reply
}

// Setup is the provider and model fx uses when den passes no flags.
type Setup struct {
	Provider string `json:"model_source"`
	Model    string `json:"model"`
	// Providers are the ones the agent has signed in to.
	Providers []string `json:"connected_providers"`
}

// Status reads fx's setup, as for provider when it is not empty.
func Status(ctx context.Context, cfg config.Config, provider string) (Setup, error) {
	var s Setup
	if err := query(ctx, cfg, provider, &s, "status", "--json"); err != nil {
		return s, err
	}

	// fx names only the configured provider it uses, so the others come
	// from its settings.
	var settings struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(cfg.Sessions)), "settings.json"))
	json.Unmarshal(data, &settings)
	for _, id := range slices.Sorted(maps.Keys(settings.Providers)) {
		if !slices.Contains(s.Providers, id) {
			s.Providers = append(s.Providers, id)
		}
	}
	return s, nil
}

// Models lists the models fx offers for provider, or for its own provider
// when provider is empty.
func Models(ctx context.Context, cfg config.Config, provider string) ([]string, error) {
	var list struct {
		IDs []string `json:"ids"`
	}
	err := query(ctx, cfg, provider, &list, "models", "--json")
	return list.IDs, err
}

// Offered lists provider's models even when fx has no model saved for it,
// which it otherwise refuses.
func Offered(ctx context.Context, cfg config.Config, provider string) ([]string, error) {
	var list struct {
		IDs []string `json:"ids"`
	}
	err := queryWith(ctx, cfg, provider, []string{"FX_MODEL=any"}, &list, "models", "--json")
	return list.IDs, err
}

// query runs a quick fx command that prints JSON and reads it into v.
func query(ctx context.Context, cfg config.Config, provider string, v any, args ...string) error {
	return queryWith(ctx, cfg, provider, nil, v, args...)
}

func queryWith(ctx context.Context, cfg config.Config, provider string, env []string, v any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd, err := proc.Command(ctx, cfg, cfg.FX, args...)
	if err != nil {
		return err
	}
	cmd.Dir = cfg.Space
	if provider != "" {
		cmd.Env = append(cmd.Env, "FX_PROVIDER="+provider)
	}
	cmd.Env = append(cmd.Env, env...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("fx %s: %v: %s", args[0], err, lastLine(stderr.String()))
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("fx %s: %v", args[0], err)
	}
	return nil
}

func lastLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	return text
}

type askResult struct {
	FinalOutput string `json:"final_output"`
	Output      string `json:"output"`
	SessionID   string `json:"session_id"`
	Usage       *struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
	} `json:"usage"`
}

func value(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func (r askResult) output() string {
	if s := strings.TrimSpace(r.FinalOutput); s != "" {
		return s
	}
	return strings.TrimSpace(r.Output)
}

// parse reads fx ask --json output: one JSON object, possibly after other
// lines.
func parse(stdout []byte) (askResult, error) {
	var r askResult
	data := bytes.TrimSpace(stdout)
	if len(data) == 0 {
		return r, errors.New("fx printed no result")
	}
	if json.Unmarshal(data, &r) == nil {
		return r, nil
	}

	if i := bytes.LastIndexByte(data, '\n'); i >= 0 && json.Unmarshal(data[i+1:], &r) == nil {
		return r, nil
	}
	return r, errors.New("fx printed a result den cannot read")
}

// tail keeps the last limit bytes written to it.
type tail struct {
	limit int
	buf   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.buf)) }

// lines calls emit with each complete line written to it.
type lines struct {
	emit    func(string)
	partial []byte
}

func (l *lines) Write(p []byte) (int, error) {
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(l.partial[:i])); line != "" {
			l.emit(line)
		}
		l.partial = l.partial[i+1:]
	}
	return len(p), nil
}

// providerFailure finds an error from the model provider that another
// model may not run into: no access, no credits or quota, a rate limit, or
// a failure on the provider's side.
var providerFailure = regexp.MustCompile(`\bHTTP (40[123]|429|5\d\d)\b`)

// ProviderFailed reports whether fx stopped because the model provider
// refused or failed, by the last line it wrote, so another model may answer.
func ProviderFailed(stderr string) bool {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	return providerFailure.MatchString(lines[len(lines)-1])
}
