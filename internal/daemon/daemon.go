// Package daemon runs den's scheduler and Telegram chat, and serves the CLI
// over a unix socket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/chat"
	"github.com/unsady/den/internal/codex"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/fetch"
	"github.com/unsady/den/internal/gitproxy"
	"github.com/unsady/den/internal/google"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/mail"
	"github.com/unsady/den/internal/models"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/search"
	"github.com/unsady/den/internal/transcribe"
	"github.com/unsady/den/internal/usage"
)

const tick = 15 * time.Second

// defaultJobs are added when the job store is first created.
var defaultJobs = []jobs.Job{
	{Name: "reflect", Schedule: "0 4 * * *", Prompt: "Use the reflect skill."},
}

type daemon struct {
	ctx    context.Context
	cfg    config.Config
	store  *jobs.Store
	runs   *jobs.Runs
	usage  *usage.Store
	ask    *approve.Asker
	fetch  *fetch.Fetcher
	search *search.Searcher
	google *google.Google
	models *models.Models
	chat   *chat.Chat

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

func Serve(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.State, 0o755); err != nil {
		return err
	}
	// The secrets are den's alone. The folder is made here, not in the
	// image, because the state is a volume that hides what the image has.
	if err := os.MkdirAll(cfg.Secrets, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.Secrets, 0o700); err != nil {
		return err
	}

	store, created, err := jobs.Open(filepath.Join(cfg.State, "jobs.json"))
	if err != nil {
		return err
	}
	if created {
		for _, j := range defaultJobs {
			if _, err := store.Add(j, time.Now()); err != nil {
				return err
			}
		}
	}

	if _, err := notify.Load(cfg); err != nil {
		log.Printf("notifications are off until secrets arrive: %v", err)
	}

	if err := proc.OwnHome(cfg); err != nil {
		log.Printf("agent home: %v", err)
	}
	initSpace(ctx, cfg)
	go func() {
		if err := transcribe.Prepare(ctx, cfg); err != nil {
			log.Printf("transcribe: %v", err)
		}
	}()

	ln, err := listen(cfg.Socket)
	if err != nil {
		return err
	}

	d := &daemon{
		ctx:     ctx,
		cfg:     cfg,
		store:   store,
		runs:    jobs.NewRuns(filepath.Join(cfg.State, "runs")),
		usage:   usage.Open(cfg.State),
		ask:     approve.New(cfg),
		running: map[string]bool{},
	}
	d.google = google.New(cfg)
	d.fetch = fetch.New(cfg, d.ask.Ask, d.google)
	d.fetch.Note = d.ask.Note
	go func() {
		if err := mail.New(cfg, d.ask.Ask).Serve(ctx); err != nil {
			log.Printf("mail: %v", err)
		}
	}()
	go func() {
		git := gitproxy.New(cfg, d.ask.Ask)
		git.Note = d.ask.Note
		if err := git.Serve(ctx); err != nil {
			log.Printf("git: %v", err)
		}
	}()
	// A new home folder lacks fx's stand-in Codex session.
	if err := codex.New(cfg).Stand(); err != nil {
		log.Printf("codex: %v", err)
	}
	d.models = models.New(cfg)
	if codex.New(cfg).Connected() {
		go codexModel(ctx, cfg, d.models)
	}
	go func() {
		if err := d.models.Serve(ctx.Done()); err != nil {
			log.Printf("models: %v", err)
		}
	}()
	d.search = search.New(cfg)

	srv := &http.Server{Handler: d.routes()}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	// Jobs run on the chat's models, so the chat exists before they start.
	d.chat = chat.New(cfg, d, d.usage)
	go d.loop()
	d.wg.Go(func() { d.chat.Run(ctx) })

	log.Printf("den is listening on %s", cfg.Socket)
	err = srv.Serve(ln)
	d.wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// initSpace runs `den init` as the agent, so the space belongs to it.
func initSpace(ctx context.Context, cfg config.Config) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("init space: %v", err)
		return
	}

	cmd, err := proc.Command(ctx, cfg, exe, "init")
	if err != nil {
		log.Printf("init space: %v", err)
		return
	}

	out, err := cmd.CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		log.Print(s)
	}
	if err != nil {
		log.Printf("init space: %v", err)
	}
}

func listen(socket string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(socket)

	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	// The agent's CLI connects as another user.
	if err := os.Chmod(socket, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (d *daemon) loop() {
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		d.runDue()
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *daemon) runDue() {
	due, err := d.store.TakeDue(time.Now(), d.isRunning)
	if err != nil {
		log.Printf("scheduler: %v", err)
	}
	for _, j := range due {
		d.start(j)
	}
}

func (d *daemon) isRunning(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running[id]
}

// start runs j in the background unless it is already running.
func (d *daemon) start(j jobs.Job) bool {
	d.mu.Lock()
	if d.running[j.ID] {
		d.mu.Unlock()
		return false
	}
	d.running[j.ID] = true
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			d.mu.Lock()
			delete(d.running, j.ID)
			d.mu.Unlock()
		}()

		log.Printf("job %s: started", j.Label())
		choice := d.chat.Choice(d.ctx, j.Slot)
		run := runner.Run(d.ctx, d.cfg, j, choice)
		// When the main model's provider refuses or fails, such as when its
		// quota runs out, the second model runs the job once.
		if second := d.chat.Choice(d.ctx, jobs.Second); run.Error != "" && d.ctx.Err() == nil && second != choice && runner.ProviderFailed(run.Stderr) {
			failed := lastLine(run.Stderr)
			log.Printf("job %s: %s failed, running it on %s", j.Label(), choice.Model, second.Model)
			run = runner.Run(d.ctx, d.cfg, j, second)
			run.FellBack = choice.Model + " failed: " + failed
		}
		run.Delivered = d.deliver(j, run)
		if run.InputTokens > 0 || run.OutputTokens > 0 {
			d.usage.Add("jobs", time.Now(), run.InputTokens, run.OutputTokens)
		}

		if err := d.runs.Save(run); err != nil {
			log.Printf("job %s: save run: %v", j.Label(), err)
		}
		if err := d.store.Finish(j.ID, run.Started, run.Status()); err != nil {
			log.Printf("job %s: %v", j.Label(), err)
		}
		log.Printf("job %s: %s in %s", j.Label(), run.Status(), run.Duration)
	}()
	return true
}

// ListJobs, StartJob, and RemoveJob serve the chat's /jobs.
func (d *daemon) ListJobs() []jobs.Job { return d.store.List() }

func (d *daemon) StartJob(ref string) (jobs.Job, error) {
	j, err := d.store.Get(ref)
	if err != nil {
		return j, err
	}
	if !d.start(j) {
		return j, fmt.Errorf("job %s is already running", j.Label())
	}
	return j, nil
}

func (d *daemon) RemoveJob(ref string) (jobs.Job, error) { return d.store.Remove(ref) }

func (d *daemon) SetJobSlot(ref, slot string) (jobs.Job, error) { return d.store.SetSlot(ref, slot) }

// ContextSize serves the chat's /status.
func (d *daemon) ContextSize(since time.Time) int { return d.models.Peak(since) }

// Answer passes the user's answer from the chat to a question den asked,
// and Rules, Forget, and Actions serve the chat's /log.
func (d *daemon) Answer(id, outcome string) (string, bool) { return d.ask.Answer(id, outcome) }

func (d *daemon) Rules() []approve.Rule { return d.ask.Rules() }

func (d *daemon) Forget(id string) (approve.Rule, bool) { return d.ask.Forget(id) }

func (d *daemon) Actions(since time.Time) []approve.Action { return d.ask.Actions(since) }

// deliver sends a run's reply, or its failure, to the user.
func (d *daemon) deliver(j jobs.Job, run jobs.Run) bool {
	var text string
	switch {
	case run.Error != "":
		text = fmt.Sprintf("⚠️ den job %s failed: %s", j.Label(), run.Error)
		if run.FellBack != "" {
			text += "\n↻ " + run.FellBack + ", and " + run.Model + " failed too:"
		}
		// fx says why on its last line, such as that no provider is set up.
		lines := strings.Split(strings.TrimSpace(run.Stderr), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			text += "\n" + last
		}
	case j.Silent || run.Output == "" || run.Output == runner.Silent:
		return false
	default:
		text = heading(j)
		if run.Model != "" {
			text += " · " + run.Model
		}
		if run.FellBack != "" {
			text += "\n↻ " + run.FellBack
		}
		text += "\n\n" + run.Output
	}

	if err := d.notify(text); err != nil {
		log.Printf("job %s: deliver: %v", j.Label(), err)
		return false
	}
	return true
}

// heading names the job above its reply, which would otherwise read like a
// reply in the chat.
func heading(j jobs.Job) string {
	switch {
	case j.Name == "reflect":
		return "🌙 Reflect"
	case j.Name != "":
		return "⏰ " + j.Name
	case j.Once:
		return "⏰ Reminder"
	}
	return "⏰ Scheduled job " + j.ID
}

func (d *daemon) notify(text string) error {
	// Secrets are read on each send, so they can be added after den starts.
	telegram, err := notify.Load(d.cfg)
	if err != nil {
		log.Printf("notify (not sent): %s", text)
		return err
	}
	ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
	defer cancel()
	return telegram.Send(ctx, text)
}

// AddRequest is the body of POST /jobs.
type AddRequest struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	// Slot is main or second, the chat's model the job runs on.
	Slot   string `json:"slot"`
	Silent bool   `json:"silent"`
}

// SlotRequest is the body of PATCH /jobs/{ref}.
type SlotRequest struct {
	Slot string `json:"slot"`
}

// NotifyRequest is the body of POST /notify.
type NotifyRequest struct {
	Text string `json:"text"`
}

// SendRequest is the body of POST /send. The CLI reads the file as the
// agent, so den sends only what the agent can read itself.
type SendRequest struct {
	Name    string `json:"name"`
	Caption string `json:"caption"`
	Data    []byte `json:"data"`
}

func (d *daemon) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		since, err := time.ParseDuration(r.URL.Query().Get("since"))
		if err != nil || since <= 0 {
			since = 24 * time.Hour
		}
		reply(w, http.StatusOK, d.models.Calls(time.Now().Add(-since)))
	})

	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		list, err := fetch.LoadCustom(d.cfg)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, http.StatusOK, list)
	})

	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, d.store.List())
	})

	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var req AddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		j, err := d.store.Add(jobs.Job{
			Name:     strings.TrimSpace(req.Name),
			Schedule: req.Schedule,
			Prompt:   req.Prompt,
			Slot:     req.Slot,
			Silent:   req.Silent,
		}, time.Now())
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		reply(w, http.StatusCreated, j)
	})

	mux.HandleFunc("PATCH /jobs/{ref}", func(w http.ResponseWriter, r *http.Request) {
		var req SlotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}

		j, err := d.store.SetSlot(r.PathValue("ref"), req.Slot)
		switch {
		case errors.Is(err, jobs.ErrNotFound):
			fail(w, http.StatusNotFound, err)
		case err != nil:
			fail(w, http.StatusBadRequest, err)
		default:
			reply(w, http.StatusOK, j)
		}
	})

	mux.HandleFunc("DELETE /jobs/{ref}", func(w http.ResponseWriter, r *http.Request) {
		j, err := d.store.Remove(r.PathValue("ref"))
		if err != nil {
			fail(w, http.StatusNotFound, err)
			return
		}
		reply(w, http.StatusOK, j)
	})

	mux.HandleFunc("POST /jobs/{ref}/run", func(w http.ResponseWriter, r *http.Request) {
		j, err := d.store.Get(r.PathValue("ref"))
		if err != nil {
			fail(w, http.StatusNotFound, err)
			return
		}
		if !d.start(j) {
			fail(w, http.StatusConflict, fmt.Errorf("job %s is already running", j.Label()))
			return
		}
		reply(w, http.StatusAccepted, j)
	})

	mux.HandleFunc("GET /jobs/{ref}/runs", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("ref")
		if j, err := d.store.Get(id); err == nil {
			id = j.ID
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 {
			n = 5
		}
		runs, err := d.runs.Last(id, n)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, http.StatusOK, runs)
	})

	mux.HandleFunc("POST /notify", func(w http.ResponseWriter, r *http.Request) {
		var req NotifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			fail(w, http.StatusBadRequest, errors.New("nothing to send"))
			return
		}
		if err := d.notify(req.Text); err != nil {
			fail(w, http.StatusServiceUnavailable, err)
			return
		}
		reply(w, http.StatusOK, map[string]bool{"sent": true})
	})

	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var req SendRequest
		// The data arrives base64-encoded, a third larger than the file.
		body := http.MaxBytesReader(w, r.Body, notify.MaxUpload/3*4+1<<20)
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		telegram, err := notify.Load(d.cfg)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, err)
			return
		}
		ctx, cancel := context.WithTimeout(d.ctx, 5*time.Minute)
		defer cancel()
		if err := telegram.SendFile(ctx, filepath.Base(req.Name), req.Data, req.Caption); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		reply(w, http.StatusOK, map[string]bool{"sent": true})
	})

	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if query == "" {
			fail(w, http.StatusBadRequest, errors.New("nothing to search for"))
			return
		}
		found, err := d.search.Search(r.Context(), query, min(max(n, 1), 20))
		if err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		reply(w, http.StatusOK, found)
	})

	mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) {
		var req fetch.Request
		// The body arrives base64-encoded, a third larger.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, fetch.MaxBody/3*4+1<<20)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		resp, err := d.fetch.Do(r.Context(), req)
		switch {
		case errors.Is(err, fetch.ErrRequest):
			fail(w, http.StatusBadRequest, err)
		case errors.Is(err, fetch.ErrSecret):
			fail(w, http.StatusPreconditionFailed, err)
		case errors.Is(err, approve.ErrDenied), errors.Is(err, approve.ErrExpired):
			fail(w, http.StatusForbidden, err)
		case err != nil:
			fail(w, http.StatusBadGateway, err)
		default:
			reply(w, http.StatusOK, resp)
		}
	})

	return mux
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}

// codexModel gives Codex a model in fx's settings once its account is
// connected, so fx lists and uses it.
func codexModel(ctx context.Context, cfg config.Config, m *models.Models) {
	err := m.Default("codex", func() ([]string, error) { return runner.Offered(ctx, cfg, "codex") })
	if err != nil {
		log.Printf("codex: %v", err)
	}
}

// lastLine returns the last line of text, where fx says why it stopped.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}
