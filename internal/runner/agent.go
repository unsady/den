package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unsady/den/internal/acp"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/gitproxy"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/seed"
)

// Agent is the chat's fx, which runs as fx acp from one turn to the next, so
// a message the user sends while a turn runs joins that turn between two of
// its steps. fx keeps one session per process: a turn in another session, or
// on another model, starts the process again, which takes a moment.
//
// Each fx process holds one session, and the commands it runs carry that
// session's id as DEN_SESSION, so den tells that session what came of
// them.
type Agent struct {
	cfg config.Config

	// OnDropped gets the key of a message Steer passed that fx did not
	// deliver, such as one that came as the turn was ending; the chat runs
	// it as a turn.
	OnDropped func(key string)
	// OnTurn gets the key of a message Steer passed that fx ran as a turn of
	// its own, because the turn it was meant for had just ended, and fx's
	// reply to it.
	OnTurn func(key string, reply Reply)

	mu      sync.Mutex
	conn    *acp.Conn
	stderr  *tail
	session string
	choice  Choice
}

// NewAgent returns an Agent. It starts fx at its first turn.
func NewAgent(cfg config.Config) *Agent {
	return &Agent{cfg: cfg}
}

// Ask runs req as a turn in req.Resume, the session den named, which it
// starts when fx has none by that id. Its commands get the id as
// DEN_SESSION.
func (a *Agent) Ask(ctx context.Context, req Request) (reply Reply) {
	reply = Reply{Started: time.Now()}
	defer func() { reply.Duration = time.Since(reply.Started).Round(time.Second) }()

	ctx, cancel := context.WithTimeout(ctx, a.cfg.JobTimeout)
	defer cancel()

	conn, session, started, err := a.open(ctx, req)
	if err != nil {
		reply.Error = err.Error()
		reply.Stderr = a.stderrText()
		return reply
	}
	reply.SessionID, reply.New = session, started

	prompt := acp.Prompt{Text: req.Prompt}
	for _, path := range req.Images {
		data, err := os.ReadFile(path)
		if err != nil {
			reply.Error = fmt.Sprintf("image: %v", err)
			return reply
		}
		prompt.Images = append(prompt.Images, acp.Image{MimeType: http.DetectContentType(data), Data: data})
	}

	// A model that fx cannot reach for req.GiveUp ends the turn, so the
	// user can try again rather than wait for the turn's timeout.
	prompting, giveUp := context.WithCancel(ctx)
	defer giveUp()
	var (
		down     *time.Timer
		downText string
		gaveUp   atomic.Bool
	)
	defer func() {
		if down != nil {
			down.Stop()
		}
	}()

	// The reply is what the agent wrote after its last step. What it said
	// before a step, such as "Reddit gives a stub, trying the old site", is
	// not part of the answer but says what it does, so it goes to Said.
	var last strings.Builder
	ooms := proc.OOMKills()
	// The model thinks from its first thought after a step, or after what
	// it wrote, until its next step or text.
	thinking := false
	onEvent := func(ev acp.Event) {
		switch ev.Kind {
		case acp.KindThought:
			if !thinking && req.Thinking != nil {
				req.Thinking()
			}
			thinking = true
		case acp.KindRecovery:
			// The model thinks anew once fx gets through.
			thinking = false
			if req.Recovering != nil {
				req.Recovering(ev.Text)
			}
			if req.GiveUp <= 0 {
				break
			}
			downText = ev.Text
			switch {
			case ev.Text == "" && down != nil:
				down.Stop()
				down = nil
			case ev.Text != "" && down == nil:
				down = time.AfterFunc(req.GiveUp, func() {
					gaveUp.Store(true)
					giveUp()
				})
			}
		case acp.KindText:
			thinking = false
			last.WriteString(ev.Text)
			if req.Draft != nil {
				req.Draft(last.String())
			}
		case acp.KindToolCall:
			thinking = false
			said := strings.TrimSpace(last.String())
			last.Reset()
			if said != "" && req.Draft != nil {
				req.Draft("")
			}
			if req.Progress != nil {
				req.Progress(stepLine(ev))
			}
			if said != "" && req.Said != nil {
				req.Said(said)
			}
		}
	}
	res, err := conn.Prompt(prompting, session, prompt, onEvent)
	// A model that cannot see pictures gets the prompt without them: the
	// prompt names where each is saved.
	if len(prompt.Images) > 0 && blind(err) {
		// What fx said before it refused, such as that it could not reach
		// its catalog of models, is not part of the reply.
		if last.Len() > 0 && req.Draft != nil {
			req.Draft("")
		}
		last.Reset()
		prompt.Images = nil
		prompt.Text += "\n" + noImages
		res, err = conn.Prompt(prompting, session, prompt, onEvent)
	}
	reply.Output = strings.TrimSpace(last.String())
	reply.InputTokens = int64(res.InputTokens)
	reply.OutputTokens = int64(res.OutputTokens)
	reply.Stderr = a.stderrText()

	switch {
	case gaveUp.Load() && ctx.Err() == nil:
		// fx says the cause first, such as "Gateway error · next try in
		// 8s · attempt 4"; the rest is about a try that will not come.
		cause, _, _ := strings.Cut(downText, " · ")
		reply.Error = "the model did not answer for " + strings.TrimSuffix(req.GiveUp.String(), "0s")
		if cause != "" {
			reply.Error += ": " + cause
		}
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
	}
	if err != nil {
		a.Close()
	}
	return reply
}

// noImages tells the agent why the pictures its prompt names are not
// attached.
const noImages = "[den: this session's model cannot see pictures, so den did not attach them. They are saved at the paths above.]"

// blind says whether err is fx's answer to pictures sent to a model that
// cannot see them.
func blind(err error) bool {
	var e *acp.Error
	return errors.As(err, &e) && e.Code == -32602 && strings.Contains(e.Message, "Image prompts are unavailable")
}

// Compact compacts req.Resume in place, on req's model, between turns.
func (a *Agent) Compact(ctx context.Context, req Request) (acp.Compaction, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.JobTimeout)
	defer cancel()

	conn, session, _, err := a.open(ctx, req)
	if err != nil {
		return acp.Compaction{}, err
	}
	out, err := conn.Compact(ctx, session)
	if err != nil {
		a.Close()
		return out, fmt.Errorf("compact: %w", err)
	}
	return out, nil
}

// Steer passes text to the turn that runs now, under key, which OnDropped
// and OnTurn get back. It returns acp.ErrIdle when none runs, and the caller
// keeps text for the next turn.
func (a *Agent) Steer(ctx context.Context, key, text string) error {
	a.mu.Lock()
	conn, session := a.conn, a.session
	a.mu.Unlock()
	if conn == nil {
		return acp.ErrIdle
	}
	return conn.Steer(ctx, session, key, text)
}

// Settle waits until fx has answered every message Steer passed, so one
// that fx runs as a turn of its own ends before the caller closes fx. It
// waits no longer than a turn may run.
func (a *Agent) Settle(ctx context.Context) {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, a.cfg.JobTimeout)
	defer cancel()
	conn.Settle(ctx)
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

// Drop stops fx when it holds session, so its next turn there reads what
// another agent added to it meanwhile.
func (a *Agent) Drop(session string) {
	a.mu.Lock()
	var conn *acp.Conn
	if a.session == session {
		conn = a.conn
		a.conn, a.session = nil, ""
	}
	a.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// open returns fx for req, with the session it asks for open in it. It
// starts fx again when it exited, or when req needs another session or
// model.
func (a *Agent) open(ctx context.Context, req Request) (conn *acp.Conn, session string, started bool, err error) {
	choice := Choice{Provider: req.Provider, Model: req.Model, Effort: req.Effort}

	// den names every session before its first turn, and the commands
	// fx runs tell den which session they are in.
	if req.Resume == "" {
		return nil, "", false, errors.New("den named no session for the turn")
	}

	a.mu.Lock()
	conn = a.conn
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
		return conn, a.session, false, nil
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
		return nil, "", false, err
	}
	cmd.Dir = a.cfg.Space
	// fx holds this one session until it exits. git names it to den's
	// proxy for GitHub only, so a push that waits for the user tells the
	// session what came of it.
	cmd.Env = append(cmd.Env, "DEN_SESSION="+req.Resume)
	cmd.Env = proc.GitConfig(cmd.Env, "http.http://"+gitproxy.Addr+"/.extraHeader", gitproxy.SessionHeader+": "+req.Resume)
	stderr := &tail{limit: 8 << 10}
	cmd.Stderr = stderr

	conn, err = acp.Start(ctx, cmd)
	if err != nil {
		return nil, "", false, fmt.Errorf("fx acp: %w", err)
	}
	conn.OnDropped = func(_, key string) {
		if a.OnDropped != nil {
			a.OnDropped(key)
		}
	}
	conn.OnTurn = func(session, key string, res acp.Result) {
		if a.OnTurn == nil {
			return
		}
		reply := Reply{SessionID: session, Output: strings.TrimSpace(res.Text), InputTokens: int64(res.InputTokens), OutputTokens: int64(res.OutputTokens)}
		if res.StopReason == "refused" {
			reply.Error, reply.Output = reply.Output, ""
		}
		a.OnTurn(key, reply)
	}

	session = req.Resume
	if session != "" {
		err = conn.Resume(ctx, session, a.cfg.Space)
	}
	// A session den named before its first turn is not saved yet.
	if session == "" || acp.NotFound(err) {
		// fx reads the rules when a session starts, so a new one gets the
		// notes as they are now.
		if err := seed.WriteRules(a.cfg); err != nil {
			log.Printf("rules: %v", err)
		}
		session, err = conn.NewSession(ctx, a.cfg.Space, session)
		started = true
	}
	if err != nil {
		conn.Close()
		if text := strings.TrimSpace(stderr.String()); text != "" {
			err = fmt.Errorf("%w: %s", err, text)
		}
		return nil, "", false, fmt.Errorf("fx acp: %w", err)
	}

	a.mu.Lock()
	a.conn, a.stderr, a.session, a.choice = conn, stderr, session, choice
	a.mu.Unlock()
	return conn, session, started, nil
}

func (a *Agent) stderrText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stderr == nil {
		return ""
	}
	return a.stderr.String()
}

// stepLine says a tool call as a step line, such as
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
