package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/proc"
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

	model := req.Provider + "/" + req.Model
	what := fmt.Sprintf("den map: %d items with %s", req.Items, model)
	effort := req.Effort
	if effort == "" {
		effort = "default"
		if req.Provider == "codex" {
			effort = "low"
		}
	}
	text := fmt.Sprintf("🗂 <b>den map: %d items</b>\n⚙︎ <b>%s</b> · <b>%s</b>\n%s\n%d at once\n%s",
		req.Items, html.EscapeString(req.Model), html.EscapeString(effort), html.EscapeString(req.Provider), req.Workers, html.EscapeString(req.File))
	if task := firstSentence(req.Instructions); task != "" {
		text += "\n" + html.EscapeString(task)
	}
	q := approve.Question{Text: text, HTML: true, What: what, Rule: "map " + model, Label: "den map with " + model}
	waiting, err := d.ask.Later(d.ctx, q, func(err error) { d.mapLater(req, what, err) })
	switch {
	case err != nil:
		fail(w, http.StatusServiceUnavailable, err)
	case waiting:
		reply(w, http.StatusAccepted, MapReply{Waiting: true})
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
	if d.ctx.Err() != nil {
		return
	}
	if answer != nil {
		d.report(req.Turn, fmt.Sprintf("[den: %s did not run: %v.]", what, answer), "")
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

	d.wg.Add(1)
	defer d.wg.Done()
	out, summary, err := d.agentCommand(d.ctx, req.Dir, append([]string{"map", "--run", run}, req.Args...)...)
	if d.ctx.Err() != nil {
		return
	}
	text := fmt.Sprintf("[den: the user allowed %s, and den ran it. Its output is quoted below.]\n%s", what, approve.Quote(out))
	user := ""
	if err != nil {
		text = fmt.Sprintf("[den: the user allowed %s, and it failed: %v. Its output is quoted below.]\n%s", what, err, approve.Quote(out))
		user = fmt.Sprintf("🗂 %s failed: %v", what, err)
	} else if first, _, _ := strings.Cut(summary, "\n"); first != "" {
		// The summary's first line says how many items it answered; the
		// progress before it goes to stderr.
		user = "🗂 " + first
	}
	d.report(req.Turn, text, user)
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

// report tells the chat's agent what came of an action of its turn, as a
// new turn. When the turn was not the chat's, such as a job's, or the user
// switched sessions since, den tells the user, with user or the agent's
// text when user is empty.
func (d *daemon) report(turn, agent, user string) {
	if user == "" {
		user = agent
	}
	if turn != "" && d.chat.Tell(turn, agent, user) {
		return
	}
	if err := d.notify(user); err != nil {
		log.Printf("report: %v", err)
	}
}
