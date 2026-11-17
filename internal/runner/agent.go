package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/acp"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/seed"
	"github.com/unsady/den/internal/sessions"
)

// Agent is the chat's fx, which runs as fx acp from one turn to the next, so
// a message the user sends while a turn runs joins that turn between two of
// its steps. fx keeps one session per process: a turn in another session, or
// on another model, starts the process again, which takes a moment.
//
// The commands the agent runs carry the Agent's Key as DEN_TURN, since the
// process outlives the turn; the chat tells which turn the key stands for.
type Agent struct {
	cfg config.Config
	key string

	// OnDropped gets a message Steer passed that fx did not deliver, such as
	// one that came as the turn was ending; the chat runs it as a turn.
	OnDropped func(text string)
	// OnTurn gets fx's reply to a message Steer passed that fx ran as a turn
	// of its own, because the turn it was meant for had just ended.
	OnTurn func(text string, reply Reply)

	mu      sync.Mutex
	conn    *acp.Conn
	stderr  *tail
	session string
	choice  Choice
}

// NewAgent returns the chat's Agent. It starts fx at its first turn.
func NewAgent(cfg config.Config) *Agent {
	return &Agent{cfg: cfg, key: "acp-" + rand.Text()}
}

// Key is what the commands the agent runs get as DEN_TURN.
func (a *Agent) Key() string { return a.key }

// Ask runs req as a turn, as runner.Ask does, in req.Resume or a new session.
// Images, CompactPercent, and Turn are not for an Agent: the chat compacts
// with Ask, and the turn is the Agent's Key.
func (a *Agent) Ask(ctx context.Context, req Request) (reply Reply) {
	reply = Reply{Started: time.Now()}
	defer func() { reply.Duration = time.Since(reply.Started).Round(time.Second) }()

	ctx, cancel := context.WithTimeout(ctx, a.cfg.JobTimeout)
	defer cancel()

	conn, session, err := a.open(ctx, req)
	if err != nil {
		reply.Error = err.Error()
		reply.Stderr = a.stderrText()
		reply.ProviderFailed = providerFailed(0, reply.Stderr)
		return reply
	}
	reply.SessionID = session

	prompt := acp.Prompt{Text: req.Prompt}
	for _, path := range req.Images {
		data, err := os.ReadFile(path)
		if err != nil {
			reply.Error = fmt.Sprintf("image: %v", err)
			return reply
		}
		prompt.Images = append(prompt.Images, acp.Image{MimeType: http.DetectContentType(data), Data: data})
	}

	// The reply is what the agent wrote after its last step, as fx ask
	// gives it: what it said before a step is not part of the answer.
	var last strings.Builder
	ooms := proc.OOMKills()
	res, err := conn.Prompt(ctx, session, prompt, func(ev acp.Event) {
		switch ev.Kind {
		case acp.KindText:
			last.WriteString(ev.Text)
		case acp.KindToolCall:
			last.Reset()
			if req.Progress != nil {
				req.Progress(stepLine(ev))
			}
		}
	})
	reply.Output = strings.TrimSpace(last.String())
	reply.InputTokens = int64(res.InputTokens)
	reply.OutputTokens = int64(res.OutputTokens)
	reply.Stderr = a.stderrText()

	if (reply.Output == Silent && !req.MaySilence) || reply.Output == "Done." {
		// A subagent's report came after the answer and took the last word;
		// the answer is in the session.
		if answer := sessions.Answer(a.cfg.Sessions, session, reply.Started); answer != "" {
			reply.Output = answer
		}
	}
	if IsSilent(reply.Output) {
		reply.Output = Silent
	}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		reply.Error = fmt.Sprintf("timed out after %s", a.cfg.JobTimeout)
	case ctx.Err() == context.Canceled:
		reply.Error = "stopped"
	case errors.Is(err, acp.ErrClosed) && proc.OOMKills() > ooms:
		reply.Error = fmt.Sprintf("fx ran out of memory: the kernel stopped it, and the container has %s", proc.MemoryLimit())
	case err != nil:
		reply.Error = err.Error()
	case res.StopReason == "refused":
		// fx puts why the model's provider refused in the reply's text.
		reply.Error = reply.Output
		reply.Output = ""
		reply.ProviderFailed = providerFailed(0, reply.Error)
	}
	if err != nil {
		a.Close()
	}
	return reply
}

// Steer passes text to the turn that runs now. It returns acp.ErrIdle when
// none runs, and the caller keeps text for the next turn.
func (a *Agent) Steer(ctx context.Context, text string) error {
	a.mu.Lock()
	conn, session := a.conn, a.session
	a.mu.Unlock()
	if conn == nil {
		return acp.ErrIdle
	}
	return conn.Steer(ctx, session, text)
}

// Close stops fx. The next turn starts it again.
func (a *Agent) Close() {
	a.mu.Lock()
	conn := a.conn
	a.conn = nil
	a.session = ""
	a.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// open returns fx for req, with the session it asks for open in it. It
// starts fx again when it exited, or when req needs another session or
// model.
func (a *Agent) open(ctx context.Context, req Request) (*acp.Conn, string, error) {
	choice := Choice{Provider: req.Provider, Model: req.Model, Effort: req.Effort}

	a.mu.Lock()
	conn := a.conn
	alive := conn != nil
	if alive {
		select {
		case <-conn.Done():
			alive = false
		default:
		}
	}
	if alive && a.choice == choice && req.Resume != "" && req.Resume == a.session {
		a.mu.Unlock()
		return conn, a.session, nil
	}
	a.mu.Unlock()
	a.Close()

	args := []string{"acp", "--full-access", "--system", systemPrompt}
	if choice.Provider != "" {
		args = append(args, "--provider", choice.Provider)
	}
	if choice.Model != "" {
		args = append(args, "--model", choice.Model)
	}
	if choice.Effort != "" {
		args = append(args, "--effort", choice.Effort)
	}
	// fx outlives the turn that starts it, so ctx does not bound it; Close
	// does.
	cmd, err := proc.Command(context.Background(), a.cfg, a.cfg.FX, args...)
	if err != nil {
		return nil, "", err
	}
	cmd.Dir = a.cfg.Space
	cmd.Env = append(cmd.Env, "DEN_TURN="+a.key)
	stderr := &tail{limit: 8 << 10}
	cmd.Stderr = stderr

	conn, err = acp.Start(ctx, cmd)
	if err != nil {
		return nil, "", fmt.Errorf("fx acp: %w", err)
	}
	conn.OnDropped = func(_, text string) {
		if a.OnDropped != nil {
			a.OnDropped(text)
		}
	}
	conn.OnTurn = func(session, text string, res acp.Result) {
		if a.OnTurn == nil {
			return
		}
		reply := Reply{SessionID: session, Output: strings.TrimSpace(res.Text), InputTokens: int64(res.InputTokens), OutputTokens: int64(res.OutputTokens)}
		if res.StopReason == "refused" {
			reply.Error, reply.Output = reply.Output, ""
		}
		a.OnTurn(text, reply)
	}

	session := req.Resume
	if session == "" {
		// fx reads the rules when a session starts, so a new one gets the
		// notes as they are now.
		if err := seed.WriteRules(a.cfg); err != nil {
			log.Printf("rules: %v", err)
		}
		session, err = conn.NewSession(ctx, a.cfg.Space)
	} else {
		err = conn.Resume(ctx, session, a.cfg.Space)
	}
	if err != nil {
		conn.Close()
		return nil, "", fmt.Errorf("fx acp: %w", err)
	}

	a.mu.Lock()
	a.conn, a.stderr, a.session, a.choice = conn, stderr, session, choice
	a.mu.Unlock()
	return conn, session, nil
}

func (a *Agent) stderrText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stderr == nil {
		return ""
	}
	return a.stderr.String()
}

// stepLine says a tool call as fx ask writes it to stderr, such as
// "Running ls -la" or "Reading notes/todo.md", for the chat's progress.
func stepLine(ev acp.Event) string {
	var call struct {
		Input struct {
			Command string `json:"command"`
			Path    string `json:"path"`
			URL     string `json:"url"`
			Query   string `json:"query"`
		} `json:"rawInput"`
	}
	_ = json.Unmarshal(ev.Raw, &call)

	in := call.Input
	for _, arg := range []string{in.Command, in.Path, in.URL, in.Query} {
		if arg != "" {
			return ev.Title + " " + arg
		}
	}
	return ev.Title
}
