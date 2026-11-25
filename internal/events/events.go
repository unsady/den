// Package events carries what happens in den to the app while it is
// connected: the user's messages, the agent's steps and replies, job
// results, and the questions den asks. Telegram gets the same things its own
// way; an event is only a nudge for a client that is listening now, and a
// client that missed some reads the state again.
//
// The hub also keeps a journal of each session as the user saw it: their
// messages, the replies, and job results, without what den adds for the
// agent alone.
package events

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The kinds of event.
const (
	// Message is the user's message to the chat's session, from any client.
	Message = "message"
	// Step is the running turn's latest step, such as "Reading notes.md",
	// or none while the model thinks.
	Step = "step"
	// Reply is the agent's answer to a turn.
	Reply = "reply"
	// Draft is the reply of the running turn as the agent writes it, whole
	// each time; an empty one says what it wrote went to a step instead.
	// The Reply that ends the turn takes its place, and Idle drops one
	// that none took.
	Draft = "draft"
	// Idle says the turn ended, with or without a reply.
	Idle = "idle"
	// Result is a job's reply or failure.
	Result = "result"
	// Notice is a report of den's own, such as a den map run's result, in
	// the session that started it, or in none.
	Notice = "notice"
	// Run is how far the den map runs a session started go in den's
	// background, such as "deepseek-v4.1-flash · 38 of 54"; empty once none
	// is left.
	Run = "run"
	// Ask is a question den asks before an action, and Answered says it no
	// longer waits.
	Ask      = "ask"
	Answered = "answered"
	// Settings says the model, the effort, the second model, or the
	// current session changed.
	Settings = "settings"
	// Titled gives a session the title den made for it.
	Titled = "titled"
	// File carries files the agent sent outside a turn's reply, such as
	// from a command that outlived the turn.
	File = "file"
	// Archived says a session went into the archive, or out of it when
	// Archived is false.
	Archived = "archived"
	// Deleted says a session is gone, however it was deleted: by the app,
	// in Telegram, by the agent, or as a job run with nothing to say.
	Deleted = "deleted"
)

// Event is one thing that happened. Fields that do not apply to its kind
// are empty.
type Event struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	// Key tells the event from any other, so a client that reads it twice,
	// live and in its session's messages, keeps it once.
	Key     string `json:"key"`
	Session string `json:"session,omitempty"`
	Text    string `json:"text,omitempty"`
	// Title is the session's title: a job's result carries it, as do a /bg
	// task's message and reply, and the event that titles a session.
	Title string `json:"title,omitempty"`
	// Kind is what started the session, as the app's session list says it:
	// bg on a /bg task's message and reply, job on a job's result.
	Kind string `json:"kind,omitempty"`
	// Job names the job on a job's result, as the session list does.
	Job   string `json:"job,omitempty"`
	Model string `json:"model,omitempty"`
	// Failed marks a job's failure, or a turn's.
	Failed bool `json:"failed,omitempty"`
	// Quiet marks a job's result that comes without a sound, such as at
	// night.
	Quiet bool `json:"quiet,omitempty"`
	// Retry says what the user can do about a failed turn: RetryAgain or
	// RetryContinue.
	Retry string `json:"retry,omitempty"`
	// ID is a question's, which the answer names.
	ID string `json:"id,omitempty"`
	// What is a question's one line, and Options the answers it takes:
	// allow, deny, always, and hour.
	What    string   `json:"what,omitempty"`
	Options []string `json:"options,omitempty"`
	// Source names where a question's action goes, such as "Gmail".
	Source string `json:"source,omitempty"`
	// Archived is where an Archived event put its session.
	Archived bool `json:"archived,omitempty"`
	// Files are the files a reply or a File event carries.
	Files []Attachment `json:"files,omitempty"`
	// Memory says a reply's or a result's turn changed the agent's memory.
	Memory *Memory `json:"memory,omitempty"`
}

// Memory is what a turn changed in the agent's memory: the lines the agent
// left on it, none when it left none.
type Memory struct {
	Lines []string `json:"lines,omitempty"`
}

// What the user can do about a failed turn. A turn that failed before its
// agent took a step did nothing, so den holds its messages to send again;
// one that failed later may have done part of its work, so the agent goes
// on from where it stopped.
const (
	RetryAgain    = "again"
	RetryContinue = "continue"
)

// Attachment is a file the agent sent the user, such as a chart, kept in
// its session's folder.
type Attachment struct {
	// File is its name in the folder, which keeps its extension, and Name
	// the name the agent gave it.
	File    string `json:"file"`
	Name    string `json:"name"`
	Size    int    `json:"size"`
	Caption string `json:"caption,omitempty"`
	// Source is the page it came from, such as a shop's page of a product.
	Source string `json:"source,omitempty"`
	// Widget describes it for the app to draw itself, such as a chart's
	// data.
	Widget json.RawMessage `json:"widget,omitempty"`
}

// Hub passes events to whoever listens. The zero value has no listeners and
// keeps no journal; a nil Hub drops every event.
type Hub struct {
	// Dir holds the journal, a file per session; none is kept when it is
	// empty.
	Dir string

	mu        sync.Mutex
	listeners map[chan Event]bool
}

// buffer is how many events wait for a slow listener before den drops it.
const buffer = 64

// Publish passes e to every listener. A listener whose buffer is full is
// dropped rather than holds den up: its channel closes, so it knows it
// missed events and reads the state anew.
func (h *Hub) Publish(e Event) {
	if h == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Key == "" {
		e.Key = rand.Text()[:16]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keep(e)
	for l := range h.listeners {
		select {
		case l <- e:
			continue
		default:
		}
		// A draft is whole each time, so a listener with no room for one
		// misses nothing the next does not bring.
		if e.Type != Draft {
			delete(h.listeners, l)
			close(l)
		}
	}
}

// Listen returns a channel of the events from now on, and a function that
// stops them and closes it.
func (h *Hub) Listen() (<-chan Event, func()) {
	l := make(chan Event, buffer)
	h.mu.Lock()
	if h.listeners == nil {
		h.listeners = map[chan Event]bool{}
	}
	h.listeners[l] = true
	h.mu.Unlock()

	return l, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.listeners[l] {
			delete(h.listeners, l)
			close(l)
		}
	}
}

// keep adds e to its session's journal, when it is one of the kinds kept.
func (h *Hub) keep(e Event) {
	if h.Dir == "" || e.Session == "" || e.Type != Message && e.Type != Reply && e.Type != Result && e.Type != File && e.Type != Notice {
		return
	}

	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		log.Printf("events: journal: %v", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(h.Dir, e.Session+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("events: journal: %v", err)
		return
	}
	defer f.Close()

	e.Session = ""
	if err := json.NewEncoder(f).Encode(e); err != nil {
		log.Printf("events: journal: %v", err)
	}
}

// Forget deletes the journal of session and its files.
func (h *Hub) Forget(session string) {
	if h == nil || h.Dir == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range []string{session + ".jsonl", session + ".files"} {
		if err := os.RemoveAll(filepath.Join(h.Dir, name)); err != nil {
			log.Printf("events: journal: %v", err)
		}
	}
}

// SaveFile keeps data with session, under a name of its own that keeps
// name's extension, and returns that name.
func (h *Hub) SaveFile(session, name string, data []byte) (string, error) {
	dir := filepath.Join(h.Dir, session+".files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	file := rand.Text()[:12] + strings.ToLower(filepath.Ext(name))
	if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
		return "", err
	}

	return file, nil
}

// FilePath is where session keeps file.
func (h *Hub) FilePath(session, file string) string {
	return filepath.Join(h.Dir, session+".files", file)
}

// Journal returns the kept events of session, oldest first. It returns an
// error that matches os.ErrNotExist when the session has none.
func Journal(dir, session string) ([]Event, error) {
	f, err := os.Open(filepath.Join(dir, session+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var list []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var e Event
		if json.Unmarshal(scanner.Bytes(), &e) == nil {
			e.Session = session
			list = append(list, e)
		}
	}
	return list, scanner.Err()
}

// Search returns the kept events of the sessions keep takes whose text
// holds each of words, in any case, the latest first, at most limit of
// them. Words are taken apart by spaces; none finds nothing. A journal that
// cannot be read is left out, so one session's damage does not stop search.
func Search(ctx context.Context, dir, words string, limit int, keep func(session string) bool) ([]Event, error) {
	terms := strings.Fields(strings.ToLower(words))
	if len(terms) == 0 {
		return nil, nil
	}

	names, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var found []Event
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session := strings.TrimSuffix(filepath.Base(name), ".jsonl")
		if !keep(session) {
			continue
		}

		list, err := Journal(dir, session)
		if err != nil {
			// A session deleted meanwhile has nothing to find.
			if !errors.Is(err, os.ErrNotExist) {
				log.Printf("search: %s: %v", session, err)
			}
			continue
		}
		for _, e := range list {
			if holds(strings.ToLower(e.Text), terms) {
				found = append(found, e)
			}
		}
	}

	slices.SortFunc(found, func(a, b Event) int { return b.At.Compare(a.At) })
	if len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

// holds reports whether text holds each of terms.
func holds(text string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(text, t) {
			return false
		}
	}
	return true
}
