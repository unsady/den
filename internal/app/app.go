// Package app serves den to its app over HTTP: the chat, its sessions and
// models, the questions den asks, and a stream of what happens.
//
// Every request carries the token that den app pair printed. den keeps only
// its hash, among its secrets, so the agent can neither read it nor find it
// in a file. The server listens on every address in the container; the host
// publishes the port only on the address den app pair chose, such as the
// machine's Tailscale address.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/chat"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/sessions"
)

// Port is where the server listens in the container.
const Port = 4400

// tokenFile holds the hash of the app's token, among den's secrets.
const tokenFile = "app_token"

// Chat is what the app does with the chat.
type Chat interface {
	Post(ctx context.Context, text, session string)
	Stop(ctx context.Context) bool
	Open(ctx context.Context, session string) error
	Setup(ctx context.Context) (chat.Setup, error)
	Models(ctx context.Context, provider string) ([]chat.Model, error)
	ChooseModel(ctx context.Context, provider, model string) error
	ChooseEffort(ctx context.Context, level string) error
	ChooseSecond(ctx context.Context, provider, model string) error
	ChooseSecondEffort(ctx context.Context, level string) error
}

// Asker is what the app does with the questions den asks.
type Asker interface {
	Pending() []events.Event
	Answer(id, outcome string) (string, bool)
}

type Server struct {
	cfg    config.Config
	chat   Chat
	ask    Asker
	events *events.Hub
	// JobSessions marks the sessions of job runs in the list of sessions.
	JobSessions func() map[string]bool

	mu sync.Mutex
	// results are the latest job results and notices, for an app that was
	// away when they came.
	results []events.Event
}

// keptResults is how many results and notices the server keeps.
const keptResults = 50

func New(cfg config.Config, chat Chat, ask Asker, hub *events.Hub) *Server {
	return &Server{cfg: cfg, chat: chat, ask: ask, events: hub}
}

// Serve listens on addr until ctx ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go s.keep(ctx)

	log.Printf("app: listening on %s", addr)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// keep holds on to the latest results and notices until ctx ends.
func (s *Server) keep(ctx context.Context) {
	list, stop := s.events.Listen()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-list:
			if e.Type != events.Result && e.Type != events.Notice {
				continue
			}
			s.mu.Lock()
			s.results = append(s.results, e)
			if len(s.results) > keptResults {
				s.results = s.results[len(s.results)-keptResults:]
			}
			s.mu.Unlock()
		}
	}
}

// Pair makes a new token for the app, which replaces the one before, and
// returns it. Only its hash is kept.
func Pair(cfg config.Config) (string, error) {
	token := rand.Text()
	sum := sha256.Sum256([]byte(token))
	if err := os.MkdirAll(cfg.Secrets, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(cfg.Secrets, tokenFile), []byte(hex.EncodeToString(sum[:])), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// Unpair forgets the token, so no app gets in until the next Pair.
func Unpair(cfg config.Config) error {
	err := os.Remove(filepath.Join(cfg.Secrets, tokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// allowed says whether r carries the token. The hash is read on every
// request, so a new pairing applies at once.
func (s *Server) allowed(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	want, err := os.ReadFile(filepath.Join(s.cfg.Secrets, tokenFile))
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(strings.TrimSpace(string(want)))) == 1
}

// answers are the app's names for the answers to a question.
var answers = map[string]string{
	"allow":  approve.Allowed,
	"deny":   approve.Denied,
	"always": approve.Always,
	"hour":   approve.ForHour,
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /events", s.stream)

	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		setup, err := s.chat.Setup(r.Context())
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, setup)
	})

	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.chat.Models(r.Context(), r.URL.Query().Get("provider"))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		reply(w, list)
	})

	choose := func(how func(ctx context.Context, req choice) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req choice
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
			if err := how(r.Context(), req); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
			s.setup(w, r)
		}
	}
	mux.HandleFunc("PUT /model", choose(func(ctx context.Context, c choice) error {
		return s.chat.ChooseModel(ctx, c.Provider, c.Model)
	}))
	mux.HandleFunc("PUT /effort", choose(func(ctx context.Context, c choice) error {
		return s.chat.ChooseEffort(ctx, c.Effort)
	}))
	mux.HandleFunc("PUT /second", choose(func(ctx context.Context, c choice) error {
		return s.chat.ChooseSecond(ctx, c.Provider, c.Model)
	}))
	mux.HandleFunc("PUT /second/effort", choose(func(ctx context.Context, c choice) error {
		return s.chat.ChooseSecondEffort(ctx, c.Effort)
	}))

	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		list, err := sessions.List(s.cfg.Sessions, 100)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		var jobs map[string]bool
		if s.JobSessions != nil {
			jobs = s.JobSessions()
		}
		out := make([]session, 0, len(list))
		for _, l := range list {
			out = append(out, session{ID: l.ID, Title: l.Title, Updated: l.Updated, Turns: l.Turns, Kind: s.kind(l.ID, jobs)})
		}
		reply(w, out)
	})

	mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
			fail(w, http.StatusBadRequest, errors.New("bad session id"))
			return
		}
		// A session from before the journal has nothing to show.
		journal, err := events.Journal(s.events.Dir, id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		list := []message{}
		for _, e := range journal {
			list = append(list, message{Mine: e.Type == events.Message, Text: e.Text, At: e.At, Failed: e.Failed})
		}
		reply(w, list)
	})

	mux.HandleFunc("POST /sessions/{id}/open", func(w http.ResponseWriter, r *http.Request) {
		if err := s.chat.Open(r.Context(), r.PathValue("id")); err != nil {
			fail(w, http.StatusNotFound, err)
			return
		}
		s.setup(w, r)
	})

	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		if err := s.chat.Open(r.Context(), ""); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		s.setup(w, r)
	})

	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
			// Session is where it goes when it is not the chat's own, such
			// as a job run's.
			Session string `json:"session"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			fail(w, http.StatusBadRequest, errors.New("nothing to send"))
			return
		}
		// The turn outlives the request; its reply comes as an event.
		s.chat.Post(context.WithoutCancel(r.Context()), req.Text, req.Session)
		reply(w, map[string]bool{"sent": true})
	})

	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"stopped": s.chat.Stop(r.Context())})
	})

	mux.HandleFunc("GET /questions", func(w http.ResponseWriter, r *http.Request) {
		reply(w, s.ask.Pending())
	})

	mux.HandleFunc("POST /questions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Answer string `json:"answer"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		outcome, ok := answers[req.Answer]
		if !ok {
			fail(w, http.StatusBadRequest, fmt.Errorf("the answer is allow, deny, always, or hour, not %q", req.Answer))
			return
		}
		text, found := s.ask.Answer(r.PathValue("id"), outcome)
		if !found {
			fail(w, http.StatusGone, errors.New("this question no longer waits"))
			return
		}
		reply(w, map[string]string{"text": text})
	})

	mux.HandleFunc("GET /results", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		list := append([]events.Event{}, s.results...)
		s.mu.Unlock()
		reply(w, list)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowed(r) {
			fail(w, http.StatusUnauthorized, errors.New("pair the app with den app pair"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// heartbeat keeps the stream open through proxies and tells the app it is
// still connected.
var heartbeat = 25 * time.Second

// stream sends the events as they happen, as server-sent events.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	list, stop := s.events.Listen()
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// The first line tells the app it is connected.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			fmt.Fprint(w, ": ping\n\n")
		case e, open := <-list:
			if !open {
				return
			}
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		flusher.Flush()
	}
}

// choice is the body of the requests that choose a model or an effort.
type choice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

// session is one session in the app's list.
type session struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Turns   int       `json:"turns"`
	// Kind is chat, job for a job run's session, or background for a /bg
	// task's.
	Kind string `json:"kind"`
}

// message is one message of a session as the user saw it.
type message struct {
	// Mine marks the user's message; the others are den's.
	Mine   bool      `json:"mine"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	Failed bool      `json:"failed,omitempty"`
}

// kind tells a job run's session and a /bg task's from the chat's: a /bg
// task's journal starts with the task, named.
func (s *Server) kind(id string, jobs map[string]bool) string {
	if jobs[id] {
		return "job"
	}
	journal, _ := events.Journal(s.events.Dir, id)
	if len(journal) > 0 && journal[0].Type == events.Message && journal[0].Title != "" {
		return "background"
	}
	return "chat"
}

// setup answers with the chat's setup, after a change to it.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	setup, err := s.chat.Setup(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	reply(w, setup)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
