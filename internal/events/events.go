// Package events carries what happens in den to the app while it is
// connected: the user's messages, the agent's steps and replies, job
// results, and the questions den asks. Telegram gets the same things its own
// way; an event is only a nudge for a client that is listening now, and a
// client that missed some reads the state again.
package events

import "sync"

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
)

// Event is one thing that happened. Fields that do not apply to its kind
// are empty.
type Event struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Text    string `json:"text,omitempty"`
	// Title names a job above its result.
	Title string `json:"title,omitempty"`
	Model string `json:"model,omitempty"`
	// Failed marks a job's failure, or a turn's.
	Failed bool `json:"failed,omitempty"`
	// ID is a question's, which the answer names.
	ID string `json:"id,omitempty"`
	// What is a question's one line, and Options the answers it takes.
	What    string   `json:"what,omitempty"`
	Options []string `json:"options,omitempty"`
}

// Hub passes events to whoever listens. The zero value has no listeners and
// is ready to use; a nil Hub drops every event.
type Hub struct {
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
	h.mu.Lock()
	defer h.mu.Unlock()
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
