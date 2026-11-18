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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unsady/den/internal/app"
	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/chat"
	"github.com/unsady/den/internal/claude"
	"github.com/unsady/den/internal/codex"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/fetch"
	"github.com/unsady/den/internal/gitproxy"
	"github.com/unsady/den/internal/google"
	"github.com/unsady/den/internal/grok"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/mail"
	"github.com/unsady/den/internal/models"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/search"
	"github.com/unsady/den/internal/seed"
	"github.com/unsady/den/internal/sessions"
	"github.com/unsady/den/internal/transcribe"
	"github.com/unsady/den/internal/usage"
)

const tick = 15 * time.Second

// defaultJobs are den's own jobs: the nightly review, and the tidying of
// the space after it.
var defaultJobs = []jobs.Job{
	{Name: "reflect", Schedule: "0 4 * * *", Prompt: "Use the reflect skill."},
	{Name: "tidy", Schedule: "0 5 * * *", Prompt: "Use the tidy skill."},
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
	// events tell the app what happens.
	events *events.Hub

	mu      sync.Mutex
	running map[string]bool
	// grants are the den map runs the user allowed, by the grant den
	// started each with.
	grants map[string]MapRequest
	// maps are the den map runs going on in den's background, by grant,
	// and mapsChanged says the chat's message about them is out of date.
	maps        map[string]*mapRun
	mapsChanged bool
	mapsShown   bool
	wg          sync.WaitGroup
	// slots limit how many jobs run at once.
	slots chan struct{}
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
	if err := addDefaultJobs(store, filepath.Join(cfg.State, "default-jobs"), created); err != nil {
		return err
	}

	if _, err := notify.Load(cfg); err != nil {
		log.Printf("notifications are off until secrets arrive: %v", err)
	}

	if err := proc.OwnHome(cfg); err != nil {
		log.Printf("agent home: %v", err)
	}
	initSpace(ctx, cfg)
	if err := seed.WriteRules(cfg); err != nil {
		log.Printf("rules: %v", err)
	}
	go func() {
		if err := transcribe.Prepare(ctx, cfg); err != nil {
			log.Printf("transcribe: %v", err)
		}
	}()
	if c := claude.New(cfg); c.Connected() {
		if err := c.Register(); err != nil {
			log.Printf("claude: %v", err)
		}
		go func() {
			if err := c.Install(ctx); err != nil {
				log.Printf("claude: %v", err)
			}
		}()
	}

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
		grants:  map[string]MapRequest{},
		maps:    map[string]*mapRun{},
		slots:   make(chan struct{}, jobSlots(cfg)),
	}
	// Jobs run on the chat's models, and what the user allowed later goes
	// to the chat's agent, so the chat exists before either starts.
	d.chat = chat.New(cfg, d, d.usage)
	d.events = d.chat.Events
	d.ask.Events = d.events
	d.runs.Forget = d.forgetSession
	d.google = google.New(cfg)
	later := func(q approve.Question, then func(error)) (bool, error) { return d.ask.Later(ctx, q, then) }
	d.fetch = fetch.New(cfg, later, d.google)
	d.fetch.Note = d.ask.Note
	d.fetch.Report = d.report
	go func() {
		m := mail.New(cfg, later)
		m.Report = d.report
		m.Turn = func() string { return d.chat.Current() }
		if err := m.Serve(ctx); err != nil {
			log.Printf("mail: %v", err)
		}
	}()
	go func() {
		git := gitproxy.New(cfg, later)
		git.Note = d.ask.Note
		git.Report = d.report
		git.Turn = func() string { return d.chat.Current() }
		if err := git.Serve(ctx); err != nil {
			log.Printf("git: %v", err)
		}
	}()
	// A new home folder lacks fx's stand-in Codex session.
	if err := codex.New(cfg).Stand(); err != nil {
		log.Printf("codex: %v", err)
	}
	if err := grok.New(cfg).Stand(); err != nil {
		log.Printf("grok: %v", err)
	}
	d.models = models.New(cfg)
	if codex.New(cfg).Connected() {
		go subscriptionModel(ctx, cfg, d.models, "codex")
	}
	if grok.New(cfg).Connected() {
		go subscriptionModel(ctx, cfg, d.models, "grok")
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

	go d.loop()
	d.wg.Go(func() { d.chat.Run(ctx) })
	go func() {
		a := app.New(cfg, d.chat, d.ask, d.events)
		a.JobSessions = d.runs.Sessions
		if err := a.Serve(ctx, fmt.Sprintf(":%d", app.Port)); err != nil {
			log.Printf("app: %v", err)
		}
	}()

	log.Printf("den is listening on %s", cfg.Socket)
	err = srv.Serve(ln)
	d.wg.Wait()
	d.ask.Drain()
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

// jobSlots is how many jobs may run at once.
func jobSlots(cfg config.Config) int {
	if cfg.Jobs > 0 {
		return cfg.Jobs
	}
	return 2
}

func (d *daemon) isRunning(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running[id]
}

// acquire waits for a free slot to run j, and says false when den stops
// first.
func (d *daemon) acquire(j jobs.Job) bool {
	select {
	case d.slots <- struct{}{}:
		return true
	default:
	}

	log.Printf("job %s: waiting for a free slot (%d running)", j.Label(), len(d.slots))
	select {
	case d.slots <- struct{}{}:
		return true
	case <-d.ctx.Done():
		return false
	}
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

		// The job counts as running while it waits, so it does not start twice.
		if !d.acquire(j) {
			return
		}
		defer func() { <-d.slots }()

		log.Printf("job %s: started", j.Label())
		choice := d.chat.Choice(d.ctx, j.Slot)
		run := runner.Run(d.ctx, d.cfg, j, choice)
		run.Delivered = d.deliver(j, run, choice)
		// A silent run leaves nothing to go on with, so its session goes
		// now instead of with the run's record.
		if silent(j, run) && run.SessionID != "" {
			if err := d.deleteJobSession(run.SessionID); err != nil {
				log.Printf("job %s: delete session: %v", j.Label(), err)
			} else {
				run.SessionID = ""
			}
		}
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

// ListJobs serves the chat's /jobs.
func (d *daemon) ListJobs() []jobs.Job { return d.store.List() }

// ContextSize serves the chat's /status.
func (d *daemon) ContextSize(since time.Time) int { return d.models.Peak(since) }

// JobSessions serves the chat's /sessions, which keeps job runs apart.
func (d *daemon) JobSessions() map[string]bool { return d.runs.Sessions() }

// deleteSession deletes the fx session id and makes the chat forget it. On
// an error it returns the HTTP status that suits it.
func (d *daemon) deleteSession(ctx context.Context, id string) (int, error) {
	list, err := sessions.List(d.cfg.Sessions, 1<<20)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !slices.ContainsFunc(list, func(s sessions.Summary) bool { return s.ID == id }) {
		return http.StatusNotFound, fmt.Errorf("no session %s", id)
	}

	if err := d.chat.Forget(id); err != nil {
		return http.StatusConflict, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := runner.DeleteSession(ctx, d.cfg, id); err != nil {
		return http.StatusInternalServerError, err
	}
	return http.StatusOK, nil
}

// LastContext serves the chat's /compact.
func (d *daemon) LastContext(since time.Time) int { return d.models.Last(since) }

// Answer passes the user's answer from the chat to a question den asked,
// and Rules, Forget, and Actions serve the chat's /log.
func (d *daemon) Answer(id, outcome string) (string, bool) { return d.ask.Answer(id, outcome) }

func (d *daemon) Rules() []approve.Rule { return d.ask.Rules() }

func (d *daemon) Forget(id string) (approve.Rule, bool) { return d.ask.Forget(id) }

func (d *daemon) Actions(since time.Time) []approve.Action { return d.ask.Actions(since) }

// deliver sends a run's reply, or its failure, to the user.
func (d *daemon) deliver(j jobs.Job, run jobs.Run, choice runner.Choice) bool {
	var text string
	switch {
	case run.Error != "":
		text = fmt.Sprintf("⚠️ den job %s failed: %s", j.Label(), run.Error)
		// fx says why on its last line, such as that no provider is set up.
		lines := strings.Split(strings.TrimSpace(run.Stderr), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			text += "\n" + last
		}
	case silent(j, run):
		return false
	default:
		text = heading(j)
		if run.Model != "" {
			text += " · " + run.Model
		}
		text += "\n\n" + run.Output
	}

	result := run.Output
	if run.Error != "" {
		result = text
	}
	// The run's session goes by the job's name in the app, as its title
	// and its short name, not by fx's title made from the prompt.
	name := jobName(j)
	if run.SessionID != "" {
		d.events.Name(run.SessionID, name, name)
	}
	d.events.Publish(events.Event{Type: events.Result, Session: run.SessionID, Title: name, Name: name, Model: run.Model, Text: result, Failed: run.Error != ""})

	// A reply to the job's message continues its session in the chat.
	ids, err := d.send(text, quietly(j, time.Now()))
	if err != nil {
		log.Printf("job %s: deliver: %v", j.Label(), err)
		return false
	}
	d.chat.Sent(ids, run.SessionID, heading(j), &choice)
	return true
}

// forgetSession deletes the fx session of a job run whose record den
// dropped, unless the user went on in it from the chat.
func (d *daemon) forgetSession(id string) {
	list, err := sessions.List(d.cfg.Sessions, 1<<20)
	if err != nil {
		log.Printf("job session %s: %v", id, err)
		return
	}
	i := slices.IndexFunc(list, func(s sessions.Summary) bool { return s.ID == id })
	if i < 0 || list[i].Turns > 1 {
		return
	}
	if err := d.deleteJobSession(id); err != nil {
		log.Printf("job session %s: %v", id, err)
	}
}

func (d *daemon) deleteJobSession(id string) error {
	ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
	defer cancel()
	return runner.DeleteSession(ctx, d.cfg, id)
}

// silent says whether a run that did not fail has nothing for the user.
func silent(j jobs.Job, run jobs.Run) bool {
	return run.Error == "" && (j.Silent || run.Output == "" || run.Output == runner.Silent)
}

// heading names the job above its reply, which would otherwise read like a
// reply in the chat.
// jobName is a job's short name: its own, or what kind of job it is.
func jobName(j jobs.Job) string {
	switch {
	case j.Name != "":
		return j.Name
	case j.Once:
		return "reminder"
	}
	return "job " + j.ID
}

func heading(j jobs.Job) string {
	switch {
	case j.Name == "reflect":
		return "🌙 Reflect"
	case j.Name == "tidy":
		return "🧹 Tidy"
	case j.Name != "":
		return "⏰ " + j.Name
	case j.Once:
		return "⏰ Reminder"
	}
	return "⏰ Scheduled job " + j.ID
}

func (d *daemon) notify(text string) error {
	d.events.Publish(events.Event{Type: events.Notice, Text: text})
	_, err := d.send(text, false)
	return err
}

// quietly says whether a job's reply arrives without a sound: den's nightly
// jobs always, and other repeating jobs at night. A one-time reminder
// rings, since the user asked for it at that time.
func quietly(j jobs.Job, now time.Time) bool {
	switch {
	case j.Once:
		return false
	case j.Name == "reflect", j.Name == "tidy":
		return true
	}
	hour := now.Hour()
	return hour >= 23 || hour < 8
}

// send is notify that returns the ids of the messages it sent, quiet ones
// without a sound.
func (d *daemon) send(text string, quiet bool) ([]int64, error) {
	// Secrets are read on each send, so they can be added after den starts.
	telegram, err := notify.Load(d.cfg)
	if err != nil {
		log.Printf("notify (not sent): %s", text)
		return nil, err
	}
	ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
	defer cancel()
	if quiet {
		return telegram.SendQuiet(ctx, text)
	}
	return telegram.SendIDs(ctx, text)
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

// ProgressRequest is the body of POST /progress: what a running command
// says about itself, shown under its step in the chat.
type ProgressRequest struct {
	// Turn is the DEN_TURN den gave the chat's agent.
	Turn string `json:"turn"`
	Text string `json:"text"`

	// Run is the grant of a den map run in den's background, and the rest
	// say how far it is.
	Run    string `json:"run,omitempty"`
	Done   int    `json:"done,omitempty"`
	Total  int    `json:"total,omitempty"`
	Failed int    `json:"failed,omitempty"`
}

// MapRequest is the body of POST /map: a den map run the user allows before
// it spends their subscription.
type MapRequest struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	Workers      int    `json:"workers"`
	Items        int    `json:"items"`
	File         string `json:"file"`
	Instructions string `json:"instructions"`

	// Args and Dir are the command's arguments and folder, which den runs
	// again once the user allows it.
	Args []string `json:"args,omitempty"`
	Dir  string   `json:"dir,omitempty"`
	// Turn is the DEN_TURN of the chat's agent that ran the command, which
	// den tells what came of it.
	Turn string `json:"turn,omitempty"`
	// Run is the grant of the run den started after the user allowed it.
	Run string `json:"run,omitempty"`
}

// MapReply answers POST /map: run now, or the user is asked and den runs
// it once they allow it.
type MapReply struct {
	Allowed bool `json:"allowed,omitempty"`
	Waiting bool `json:"waiting,omitempty"`
	// Started says den runs it in its background for the chat's agent, and
	// tells the agent what came of it.
	Started bool `json:"started,omitempty"`
}

// SendRequest is the body of POST /send. The CLI reads the file as the
// agent, so den sends only what the agent can read itself.
type SendRequest struct {
	Name    string `json:"name"`
	Caption string `json:"caption"`
	Data    []byte `json:"data"`
	// Turn is the DEN_TURN of the agent that sent it: the file goes with
	// that turn's reply, or where the turn answered.
	Turn string `json:"turn,omitempty"`
	// Widget describes the file for the app to draw itself, such as a
	// chart's data; Telegram gets the file.
	Widget json.RawMessage `json:"widget,omitempty"`
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
		// A repeating job goes on working after the chat that added it, so
		// /log shows it.
		if !j.Once {
			d.ask.Note(fmt.Sprintf("Job %s added: %s", j.Label(), j.Schedule), approve.Went)
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
		if !j.Once {
			d.ask.Note(fmt.Sprintf("Job %s removed", j.Label()), approve.Went)
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
		// A removed job's runs are found by its ID, which names a folder.
		if !jobID.MatchString(id) {
			fail(w, http.StatusBadRequest, errors.New("not a job"))
			return
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

	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if status, err := d.deleteSession(r.Context(), id); err != nil {
			fail(w, status, err)
			return
		}
		reply(w, http.StatusOK, map[string]string{"id": id})
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

	mux.HandleFunc("POST /progress", func(w http.ResponseWriter, r *http.Request) {
		var req ProgressRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if req.Run != "" {
			d.runProgress(req.Run, req.Done, req.Total, req.Failed)
		} else {
			d.chat.Note(req.Turn, req.Text)
		}
		reply(w, http.StatusOK, map[string]bool{"shown": true})
	})

	mux.HandleFunc("POST /map", d.serveMap)

	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var req SendRequest
		// The data arrives base64-encoded, a third larger than the file.
		body := http.MaxBytesReader(w, r.Body, notify.MaxUpload/3*4+1<<20)
		if err := json.NewDecoder(body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(d.ctx, 5*time.Minute)
		defer cancel()
		upload := chat.Upload{Upload: notify.Upload{Name: filepath.Base(req.Name), Data: req.Data, Caption: req.Caption}, Widget: req.Widget}
		if err := d.chat.Send(ctx, req.Turn, upload); errors.Is(err, notify.ErrNotConfigured) {
			fail(w, http.StatusServiceUnavailable, err)
			return
		} else if err != nil {
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

// jobID is what a job's ID looks like.
var jobID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// firstSentence is the opening of instructions, enough to tell what a run
// is for: its first line, up to its first sentence, at most 160 runes.
func firstSentence(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if i := strings.Index(line, ". "); i > 0 {
		line = line[:i+1]
	}
	return clipText(line, 160)
}

// clipText keeps the first n runes of text, marking a cut with an ellipsis.
func clipText(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}

// subscriptionModel gives a subscription's provider, codex or grok, a
// model in fx's settings once its account is connected, so fx lists and
// uses it.
func subscriptionModel(ctx context.Context, cfg config.Config, m *models.Models, provider string) {
	err := m.Default(provider, func() ([]string, error) { return runner.Offered(ctx, cfg, provider) })
	if err != nil {
		log.Printf("%s: %v", provider, err)
	}
}

// lastLine returns the last line of text, where fx says why it stopped.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}
