// Package runner runs noninteractive fx requests for jobs and chat.
package runner

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/proc"
)

// systemPrompt takes the place of fx's own base prompt, which is written
// for a coding agent: it asks for status reports and Markdown, where the
// user wants an assistant's result in plain text. fx keeps its tool, skill,
// project, and runtime context.
//
//go:embed system.md
var systemPrompt string

// Silent is the reply that tells den a job has nothing to deliver.
const Silent = "SILENT"

// IsSilent reports whether a reply asks den to send nothing: Silent alone,
// or Silent on the reply's last line after a status the model was told to
// leave out.
func IsSilent(reply string) bool {
	reply = strings.TrimSpace(reply)
	last := reply[strings.LastIndex(reply, "\n")+1:]
	return strings.TrimSpace(last) == Silent
}

const deliveredNote = "This is the scheduled den job %s. It runs in a fresh session without the user's chat history. " +
	"Your final reply is sent to the user as a notification, so keep it short and self-contained. " +
	"Reply with exactly the English word " + Silent + ", untranslated, when there is nothing worth sending."

const silentNote = "This is the scheduled den job %s. It runs in a fresh session without the user's chat history. " +
	"Your final reply is saved in the job log and is not sent to the user."

// Prompt is the request fx receives for j. The job's own words come first,
// because fx names a session after the start of its first message when the
// provider has no model for titles.
func Prompt(j jobs.Job) string {
	note := deliveredNote
	if j.Silent {
		note = silentNote
	}
	return j.Prompt + "\n\n[" + fmt.Sprintf(note, j.Name) + "]"
}

// Choice is a model to use with its reasoning effort. Empty fields leave
// fx's own choice.
type Choice struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// Run runs j in session, which den named and fx starts, on the model choice
// names, as turn, which its commands get as DEN_TURN.
func Run(ctx context.Context, cfg config.Config, j jobs.Job, choice Choice, turn, session string) jobs.Run {
	agent := NewAgent(cfg, turn)
	defer agent.Close()
	reply := agent.Ask(ctx, Request{Prompt: Prompt(j), Provider: choice.Provider, Model: choice.Model, Effort: choice.Effort, Resume: session})
	return jobs.Run{
		Job:       j.Name,
		Started:   reply.Started,
		Duration:  reply.Duration,
		SessionID: reply.SessionID,
		Output:    reply.Output,
		Error:     reply.Error,
		Stderr:    reply.Stderr,
		Model:     choice.Model,

		InputTokens:  reply.InputTokens,
		OutputTokens: reply.OutputTokens,
	}
}

// Request is one turn of the agent.
type Request struct {
	Prompt string
	// Provider and Model, when set, override fx's own choice for this request.
	Provider string
	Model    string
	// Effort, when set, is the model's reasoning effort, such as high.
	Effort string
	// Resume continues this session id instead of starting a new session.
	// An Agent starts the session under this id when fx has none by it.
	Resume string
	// Images are attached to the request.
	Images []string
	// MaySilence keeps a reply of Silent as it is, for a turn that need not
	// answer, instead of taking the session's last answer in its place.
	MaySilence bool
	// Progress, when set, receives each line fx writes to stderr while it
	// works, such as "Reading notes/todo.md" before a tool runs.
	Progress func(line string)
}

// Reply is what one turn produced. Error is empty on success.
type Reply struct {
	Started   time.Time
	Duration  time.Duration
	SessionID string
	Output    string
	Error     string
	Stderr    string
	// Input and Output tokens are what fx reported, 0 when it reported none.
	InputTokens  int64
	OutputTokens int64
	// New says the turn started its session.
	New bool
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

// DeleteSession deletes a saved fx session and the subagent sessions it
// owns. An fx from before `fx session delete` fails it.
func DeleteSession(ctx context.Context, cfg config.Config, id string) error {
	var deleted struct {
		ID string `json:"id"`
	}
	return query(ctx, cfg, "", &deleted, "session", "delete", id, "--json")
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

// tail keeps the last limit bytes written to it. fx acp writes to it while
// den reads it.
type tail struct {
	limit int
	mu    sync.Mutex
	buf   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
