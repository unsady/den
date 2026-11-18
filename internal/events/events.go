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
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/unsady/den/internal/sessions"
)

// The kinds of event.
const (
	// Message is the user's message to the chat's session, from any client.
	Message = "message"
	// Step is the running turn's latest step, such as "Reading notes.md".
	Step = "step"
	// Reply is the agent's answer to a turn.
	Reply = "reply"
	// Idle says the turn ended, with or without a reply.
	Idle = "idle"
	// Result is a job's reply or failure.
	Result = "result"
	// Notice is a message the agent sent the user, such as with den notify.
	Notice = "notice"
	// Ask is a question den asks before an action, and Answered says it no
	// longer waits.
	Ask      = "ask"
	Answered = "answered"
	// Settings says the model, the effort, the second model, or the
	// current session changed.
	Settings = "settings"
	// Titled gives a session the title den made for it.
	Titled = "titled"
)

// Event is one thing that happened. Fields that do not apply to its kind
// are empty.
type Event struct {
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	Session string    `json:"session,omitempty"`
	Text    string    `json:"text,omitempty"`
	// Title names a job above its result, and a /bg task above its
	// message and reply.
	Title string `json:"title,omitempty"`
	Model string `json:"model,omitempty"`
	// Failed marks a job's failure, or a turn's.
	Failed bool `json:"failed,omitempty"`
	// ID is a question's, which the answer names.
	ID string `json:"id,omitempty"`
	// What is a question's one line, and Options the answers it takes:
	// allow, deny, always, and hour.
	What    string   `json:"what,omitempty"`
	Options []string `json:"options,omitempty"`
	// Source names where a question's action goes, such as "Gmail".
	Source string `json:"source,omitempty"`
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

// buffer is how many events wait for a slow listener before it misses some.
const buffer = 64

// Publish passes e to every listener. A listener whose buffer is full
// misses it rather than holds den up.
func (h *Hub) Publish(e Event) {
	if h == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keep(e)
	for l := range h.listeners {
		select {
		case l <- e:
		default:
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

	var once sync.Once
	return l, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.listeners, l)
			h.mu.Unlock()
			close(l)
		})
	}
}

// keep adds e to its session's journal, when it is one of the kinds kept.
func (h *Hub) keep(e Event) {
	if h.Dir == "" || e.Session == "" || e.Type != Message && e.Type != Reply && e.Type != Result {
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

// Forget deletes the journal of session and its title.
func (h *Hub) Forget(session string) {
	if h == nil || h.Dir == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, name := range []string{session + ".jsonl", session + ".title"} {
		if err := os.Remove(filepath.Join(h.Dir, name)); err != nil && !os.IsNotExist(err) {
			log.Printf("events: journal: %v", err)
		}
	}
}

// Name keeps title as session's title, in place of fx's, and tells the
// listeners.
func (h *Hub) Name(session, title string) {
	if h == nil || h.Dir == "" {
		return
	}

	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		log.Printf("events: title: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(h.Dir, session+".title"), []byte(title), 0o600); err != nil {
		log.Printf("events: title: %v", err)
		return
	}
	h.Publish(Event{Type: Titled, Session: session, Text: title})
}

// Titled gives each session of list the title den made for it, where it
// made one.
func (h *Hub) Titled(list []sessions.Summary) {
	if h == nil || h.Dir == "" {
		return
	}

	for i, s := range list {
		if title, err := os.ReadFile(filepath.Join(h.Dir, s.ID+".title")); err == nil && len(title) > 0 {
			list[i].Title = string(title)
		}
	}
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
