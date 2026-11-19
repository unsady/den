package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/store"
)

// serveMap asks the user to allow a den map run. A run the user allowed for
// good goes on at once. Otherwise the command returns, so the agent's turn
// does not have to wait for the user, and den runs the command again as the
// agent once they allow it.
func (d *daemon) serveMap(w http.ResponseWriter, r *http.Request) {
	var req MapRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}

	if req.Run != "" {
		d.mu.Lock()
		granted, ok := d.grants[req.Run]
		delete(d.grants, req.Run)
		d.mu.Unlock()
		// The grant covers the run the user saw, not another one.
		if !ok || !sameRun(granted, req) {
			fail(w, http.StatusForbidden, errors.New("this run was not allowed"))
			return
		}
		reply(w, http.StatusOK, MapReply{Allowed: true})
		return
	}

	// den tells the agent's session what came of the run, so it runs only
	// for a session den has.
	s, err := d.db.Session(d.ctx, req.Session)
	if err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("den cannot tell which session runs it: DEN_SESSION names none it has: %w", err))
		return
	}

	model := req.Provider + "/" + req.Model
	what := fmt.Sprintf("den map: %d items with %s", req.Items, model)
	effort := req.Effort
	if effort == "" {
		effort = "default"
		if req.Provider == "codex" {
			effort = "low"
		}
	}
	// What the run does comes first, then what it runs on; the file is the
	// agent's business.
	text := fmt.Sprintf("🗂 Run a model on %d items", req.Items)
	if task := firstSentence(req.Instructions); task != "" {
		text += "\n" + task
	}
	text += fmt.Sprintf("\n\n⚙︎ %s · %s\nProvider: %s\nThreads: %d", req.Model, effort, req.Provider, req.Workers)
	q := approve.Question{Text: text, What: what, Source: "den map", Rule: "map " + model, Label: "den map with " + model, Session: req.Session}
	waiting, err := d.ask.Later(d.ctx, q, func(err error) { d.mapLater(req, what, err) })
	switch {
	case err != nil:
		fail(w, http.StatusServiceUnavailable, err)
	case waiting:
		reply(w, http.StatusAccepted, MapReply{Waiting: true})
	case s.Kind != store.KindJob:
		// A run goes on in den's background even when a rule allows it, so
		// the user can talk to the agent meanwhile. A job's agent runs its
		// own: its reply is the job's result, which needs the run's.
		go d.mapLater(req, what, nil)
		reply(w, http.StatusAccepted, MapReply{Started: true})
	default:
		reply(w, http.StatusOK, MapReply{Allowed: true})
	}
}

func sameRun(a, b MapRequest) bool {
	return a.Provider == b.Provider && a.Model == b.Model && a.Effort == b.Effort && a.Workers == b.Workers &&
		a.Items == b.Items && a.File == b.File && a.Instructions == b.Instructions
}

// mapLater runs the command the user allowed, as the agent, and says what
// came of it, or that the user did not allow it.
func (d *daemon) mapLater(req MapRequest, what string, answer error) {
	if answer != nil {
		d.report(req.Session, fmt.Sprintf("[den: %s did not run: %v.]", what, answer), "")
		return
	}
	if d.ctx.Err() != nil {
		return
	}

	run := rand.Text()
	d.mu.Lock()
	d.grants[run] = req
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.grants, run)
		d.mu.Unlock()
	}()
	d.runStarted(run, req.Session, req.Model, req.Items)

	d.wg.Add(1)
	defer d.wg.Done()
	out, summary, err := d.agentCommand(d.ctx, req.Dir, append([]string{"map", "--run", run}, req.Args...)...)
	d.runEnded(run)
	if d.ctx.Err() != nil {
		return
	}
	// The agent gets the run's summary alone: the progress lines only told
	// the user how far it was.
	text := fmt.Sprintf("[den: %s ran. Its output is quoted below.]\n%s", what, approve.Quote(summary))
	user := "🗂 " + what + " ran"
	if err != nil {
		text = fmt.Sprintf("[den: %s failed: %v. Its output is quoted below.]\n%s", what, err, approve.Quote(withoutProgress(out)))
		user = fmt.Sprintf("🗂 %s failed: %v", what, err)
	} else if first, _, _ := strings.Cut(summary, "\n"); first != "" {
		// The summary's first line says how many items it answered; the
		// progress before it goes to stderr.
		user = "🗂 " + first
	}
	d.report(req.Session, text, user)
}

// agentCommand runs den as the agent in dir. It returns all it printed,
// the last lines when it printed much, and its stdout apart.
func (d *daemon) agentCommand(ctx context.Context, dir string, args ...string) (string, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	cmd, err := proc.Command(ctx, d.cfg, exe, args...)
	if err != nil {
		return "", "", err
	}
	cmd.Dir = dir
	var all, stdout bytes.Buffer
	cmd.Stdout = io.MultiWriter(&all, &stdout)
	cmd.Stderr = &all
	err = cmd.Run()
	return tail(all.String(), 4000), strings.TrimSpace(stdout.String()), err
}

// tail keeps the end of text, at most n bytes, from a line's start.
func tail(text string, n int) string {
	text = strings.TrimSpace(text)
	if len(text) <= n {
		return text
	}
	text = text[len(text)-n:]
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	return "…\n" + text
}

// report tells session what came of an action its agent took, as a new
// turn there. When den no longer has the session, such as one the user
// deleted meanwhile, den tells the user user instead, and nothing when it
// is empty, such as for an action the user denied: the question already
// says so.
//
// A session comes from the agent, as DEN_SESSION or what den sets from it,
// and the agent can name any, so a session only ever says where den's
// answer goes: it must never allow an action or reach a secret.
func (d *daemon) report(session, agent, user string) {
	if d.chat.Tell(session, agent, user) {
		return
	}
	if user == "" {
		return
	}
	if err := d.notify(session, user); err != nil {
		log.Printf("report: %v", err)
	}
}

// withoutProgress drops den map's progress lines from its output.
func withoutProgress(out string) string {
	var kept []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "den map: ") || strings.HasPrefix(line, "Asking ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
