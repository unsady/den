// Package app serves den to its app over HTTP: the chat, its sessions and
// models, the questions den asks, and a stream of what happens.
//
// Every request carries a token that den app pair printed, one for each
// device. den keeps only their hashes, among its secrets, so the agent can neither read it nor find it
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
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/chat"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/memory"
	"github.com/unsady/den/internal/store"
	"github.com/unsady/den/internal/today"
)

// Port is where the server listens in the container.
const Port = 4400

// tokenFile holds the hashes of the app's tokens, one a line, among den's
// secrets.
const tokenFile = "app_token"

// Chat is what the app does with the chat.
type Chat interface {
	Post(ctx context.Context, text, session string, files []chat.File) error
	Start(ctx context.Context, text string, files []chat.File) (string, error)
	Stop(ctx context.Context) bool
	StopSession(ctx context.Context, session string) bool
	Settle(ctx context.Context, session string) error
	Retry(ctx context.Context, session string) error
	Holds(ctx context.Context, session string) bool
	Continue(ctx context.Context, session string) error
	Open(ctx context.Context, session string) error
	Setup(ctx context.Context) (chat.Setup, error)
	Models(ctx context.Context, provider string) ([]chat.Model, error)
	ChooseModel(ctx context.Context, provider, model string) error
	ChooseEffort(ctx context.Context, level string) error
	ChooseSecond(ctx context.Context, provider, model string) error
	ChooseSecondEffort(ctx context.Context, level string) error
	Sessions(ctx context.Context, shelf store.Shelf, job string, before *store.Mark, limit int) ([]store.Session, error)
	Archive(ctx context.Context, session string, archived bool) error
}

// Asker is what the app does with the questions den asks.
type Asker interface {
	Pending() []events.Event
	Answer(id, outcome string) (string, bool)
}

// Drop deletes a session and returns the HTTP status that suits its error.
type Drop func(ctx context.Context, id string) (int, error)

type Server struct {
	cfg    config.Config
	chat   Chat
	ask    Asker
	drop   Drop
	events *events.Hub

	mu sync.Mutex
	// results are the latest job results and notices, for an app that was
	// away when they came.
	results []events.Event
	// runs are the lines of the den map runs going on, by the session that
	// started them.
	runs map[string]string
}

// keptResults is how many results and notices the server keeps.
const keptResults = 50

func New(cfg config.Config, chat Chat, ask Asker, drop Drop, hub *events.Hub) *Server {
	return &Server{cfg: cfg, chat: chat, ask: ask, drop: drop, events: hub, runs: map[string]string{}}
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

// keep holds on to the latest results and notices, and the runs going on,
// until ctx ends.
func (s *Server) keep(ctx context.Context) {
	list, stop := s.events.Listen()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, open := <-list:
			// The hub dropped a listener that fell behind: listen again.
			if !open {
				stop()
				list, stop = s.events.Listen()
				continue
			}
			s.mu.Lock()
			switch {
			case e.Type == events.Run && e.Text == "":
				delete(s.runs, e.Session)
			case e.Type == events.Run:
				s.runs[e.Session] = e.Text
			case e.Type == events.Result || e.Type == events.Notice:
				s.results = append(s.results, e)
				if len(s.results) > keptResults {
					s.results = s.results[len(s.results)-keptResults:]
				}
			}
			s.mu.Unlock()
		}
	}
}

// Pair makes a new token for one more device and returns it; the tokens
// made before keep working. Only its hash is kept.
func Pair(cfg config.Config) (string, error) {
	token := rand.Text()
	sum := sha256.Sum256([]byte(token))
	if err := os.MkdirAll(cfg.Secrets, 0o700); err != nil {
		return "", err
	}

	path := filepath.Join(cfg.Secrets, tokenFile)
	kept, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	hashes := append(strings.Fields(string(kept)), hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path, []byte(strings.Join(hashes, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// Unpair forgets every token, so no app gets in until the next Pair.
func Unpair(cfg config.Config) error {
	err := os.Remove(filepath.Join(cfg.Secrets, tokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// allowed says whether r carries one of the tokens. The hashes are read on
// every request, so a new pairing applies at once.
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
	got := []byte(hex.EncodeToString(sum[:]))
	for _, line := range strings.Fields(string(want)) {
		if subtle.ConstantTimeCompare(got, []byte(line)) == 1 {
			return true
		}
	}
	return false
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

	mux.HandleFunc("GET /setup", s.setup)

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
		// ?job= keeps the runs of one job, older ones too. With ?limit= the
		// list comes in pages, each with where the next starts, which
		// ?before= takes.
		query := r.URL.Query()
		limit, paged, err := pageLimit(query.Get("limit"))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		before, err := sessionMark(query.Get("before"))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}

		// ?archived=1 lists the archive instead of the feed.
		shelf := store.Feed
		if query.Get("archived") == "1" {
			shelf = store.Archive
		}
		list, err := s.chat.Sessions(r.Context(), shelf, query.Get("job"), before, limit)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]session, 0, len(list))
		for _, l := range list {
			out = append(out, session{ID: l.ID, Kind: string(l.Kind), Parent: l.Parent, Job: l.Job, Title: l.Title, Archived: l.Archived, Updated: l.Updated})
		}
		if !paged {
			reply(w, out)
			return
		}

		page := sessionPage{Sessions: out}
		if len(list) == limit {
			last := list[len(list)-1]
			page.Before = fmt.Sprintf("%d:%s", last.Updated.UnixMilli(), last.ID)
		}
		reply(w, page)
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
		// With ?limit= the messages come in pages, the latest first, each
		// with where the one before it ends, which ?before= takes.
		query := r.URL.Query()
		limit, paged, err := pageLimit(query.Get("limit"))
		if err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}

		end := len(journal)
		if b := query.Get("before"); b != "" {
			n, err := strconv.Atoi(b)
			if err != nil || n < 0 {
				fail(w, http.StatusBadRequest, errors.New("bad before"))
				return
			}
			end = min(n, end)
		}

		start := 0
		if paged {
			start = max(0, end-limit)
		}

		list := []message{}
		for _, e := range journal[start:end] {
			list = append(list, message{Key: e.Key, Mine: e.Type == events.Message, Text: e.Text, At: e.At, Failed: e.Failed, Retry: e.Retry, Files: e.Files})
		}
		// The journal keeps an offer to retry for good: once its messages
		// went again, or the user wrote on, den holds nothing to retry.
		if n := len(list); n > 0 && end == len(journal) && list[n-1].Retry == events.RetryAgain && !s.chat.Holds(r.Context(), id) {
			list[n-1].Retry = ""
		}
		if !paged {
			reply(w, list)
			return
		}

		page := messagePage{Messages: list}
		if start > 0 {
			page.Before = strconv.Itoa(start)
		}
		reply(w, page)
	})

	mux.HandleFunc("GET /sessions/{id}/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		id, file := r.PathValue("id"), r.PathValue("file")
		if strings.ContainsAny(id+file, `/\`) || id == ".." || file == ".." {
			fail(w, http.StatusBadRequest, errors.New("bad file name"))
			return
		}
		http.ServeFile(w, r, s.events.FilePath(id, file))
	})

	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// The user confirmed the delete, so a turn that runs there stops
		// first rather than keeping the session.
		if s.chat.StopSession(r.Context(), id) {
			settle, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			err := s.chat.Settle(settle, id)
			cancel()
			if err != nil {
				fail(w, http.StatusConflict, errors.New("the turn in this session has not stopped yet"))
				return
			}
		}
		if status, err := s.drop(r.Context(), id); err != nil {
			fail(w, status, err)
			return
		}

		// A client that was in the session is in none now, so the apps
		// read the setup again.
		s.events.Publish(events.Event{Type: events.Settings})
		reply(w, map[string]string{"id": id})
	})

	archive := func(archived bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			err := s.chat.Archive(r.Context(), id, archived)
			switch {
			case errors.Is(err, store.ErrNotFound):
				fail(w, http.StatusNotFound, fmt.Errorf("no session %s", id))
				return
			case err != nil:
				fail(w, http.StatusInternalServerError, err)
				return
			}
			reply(w, map[string]any{"id": id, "archived": archived})
		}
	}
	mux.HandleFunc("POST /sessions/{id}/archive", archive(true))
	mux.HandleFunc("DELETE /sessions/{id}/archive", archive(false))

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
			// New starts a session of its own for it, whose ID the answer
			// gives.
			New bool `json:"new"`
			// Files are what the user attached, in base64.
			Files []chat.File `json:"files"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessage)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Text) == "" && len(req.Files) == 0 {
			fail(w, http.StatusBadRequest, errors.New("nothing to send"))
			return
		}
		if req.New && req.Session != "" {
			fail(w, http.StatusBadRequest, errors.New("a new message goes to no session"))
			return
		}

		// The turn outlives the request; its reply comes as an event.
		if req.New {
			session, err := s.chat.Start(context.WithoutCancel(r.Context()), req.Text, req.Files)
			if err != nil {
				fail(w, http.StatusInternalServerError, err)
				return
			}
			reply(w, map[string]any{"sent": true, "session": session})
			return
		}
		if err := s.chat.Post(context.WithoutCancel(r.Context()), req.Text, req.Session, req.Files); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, map[string]bool{"sent": true})
	})

	mux.HandleFunc("GET /memory", func(w http.ResponseWriter, r *http.Request) {
		m, err := memory.Read(s.cfg)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, m)
	})

	mux.HandleFunc("PUT /memory", func(w http.ResponseWriter, r *http.Request) {
		var m memory.Memory
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		err := memory.Write(s.cfg, m)
		switch {
		case errors.Is(err, memory.ErrChanged):
			fail(w, http.StatusConflict, err)
			return
		case err != nil:
			fail(w, http.StatusInternalServerError, err)
			return
		}

		m, err = memory.Read(s.cfg)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, m)
	})

	mux.HandleFunc("GET /today", func(w http.ResponseWriter, r *http.Request) {
		day, err := today.Read(s.cfg, time.Now())
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, day)
	})

	// Ticks the to-do at its index in the list, or unticks it, while it
	// still reads the text the app showed.
	mux.HandleFunc("PUT /today/tasks/{index}", func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil || index < 0 {
			fail(w, http.StatusBadRequest, fmt.Errorf("no to-do %q", r.PathValue("index")))
			return
		}
		var req struct {
			Text string `json:"text"`
			Done bool   `json:"done"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}

		err = today.Check(s.cfg, index, req.Text, req.Done)
		switch {
		case errors.Is(err, today.ErrChanged):
			fail(w, http.StatusConflict, err)
			return
		case err != nil:
			fail(w, http.StatusInternalServerError, err)
			return
		}

		tasks, err := today.Tasks(s.cfg)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, map[string]any{"tasks": tasks})
	})

	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		// With a session, only its turn stops; without, everything does.
		var req struct {
			Session string `json:"session"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if req.Session != "" {
			reply(w, map[string]bool{"stopped": s.chat.StopSession(r.Context(), req.Session)})
			return
		}
		reply(w, map[string]bool{"stopped": s.chat.Stop(r.Context())})
	})

	mux.HandleFunc("POST /sessions/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := s.chat.Retry(r.Context(), id)
		switch {
		case errors.Is(err, chat.ErrNothingHeld):
			fail(w, http.StatusConflict, err)
			return
		case err != nil:
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, map[string]string{"id": id})
	})

	mux.HandleFunc("POST /sessions/{id}/continue", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := s.chat.Continue(r.Context(), id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			fail(w, http.StatusNotFound, fmt.Errorf("no session %s", id))
			return
		case err != nil:
			fail(w, http.StatusInternalServerError, err)
			return
		}
		reply(w, map[string]string{"id": id})
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
	ID string `json:"id"`
	// Kind is chat, bg for a /bg task's session, or job for a job run's.
	Kind string `json:"kind"`
	// Parent is the session it was started from, if any.
	Parent string `json:"parent,omitempty"`
	// Job names the job a job run's session is of, such as
	// nightly-reflect for den's own nightly review.
	Job string `json:"job,omitempty"`
	// Title is empty until den names the session.
	Title string `json:"title,omitempty"`
	// Archived marks a session in the archive.
	Archived bool      `json:"archived,omitempty"`
	Updated  time.Time `json:"updated"`
}

// message is one message of a session as the user saw it.
type message struct {
	// Key is the event's that brought it, empty in a journal older than keys.
	Key string `json:"key,omitempty"`
	// Mine marks the user's message; the others are den's.
	Mine   bool      `json:"mine"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	Failed bool      `json:"failed,omitempty"`
	// Retry says what the user can do about a failed turn, as events.Event's.
	Retry string `json:"retry,omitempty"`
	// Files are the files den sent with it, each of which
	// GET /sessions/{id}/files/{file} returns.
	Files []events.Attachment `json:"files,omitempty"`
}

// API is the version of what den and the app say to each other. It goes up
// with each change an app built for the one before would misread, so an app
// that speaks another version tells the user which side to update.
const API = 1

// setup answers with the chat's setup and the API, after a change to it.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	setup, err := s.chat.Setup(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	runs := maps.Clone(s.runs)
	s.mu.Unlock()
	reply(w, struct {
		chat.Setup
		// Runs are the lines of the den map runs going on, by session.
		Runs map[string]string `json:"runs"`
		API  int               `json:"api"`
	}{setup, runs, API})
}

// A page of sessions or messages, and where the next one starts, empty
// after the last.
type sessionPage struct {
	Sessions []session `json:"sessions"`
	Before   string    `json:"before,omitempty"`
}

type messagePage struct {
	Messages []message `json:"messages"`
	Before   string    `json:"before,omitempty"`
}

// maxMessage is the most a message from the app may weigh: 20 MB of files,
// as much as Telegram lets den download, in base64, which is a third
// larger, with room for the text.
const maxMessage = 32 << 20

// maxPage is the most a page holds.
const maxPage = 200

// pageLimit reads ?limit=: how many a page holds, and whether one was asked.
func pageLimit(s string) (int, bool, error) {
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false, errors.New("bad limit")
	}
	return min(n, maxPage), true, nil
}

// sessionMark reads ?before= of a page of sessions: the milliseconds and
// the id where the page before ended.
func sessionMark(s string) (*store.Mark, error) {
	if s == "" {
		return nil, nil
	}
	at, id, ok := strings.Cut(s, ":")
	ms, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return nil, errors.New("bad before")
	}
	return &store.Mark{Updated: time.UnixMilli(ms), ID: id}, nil
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
