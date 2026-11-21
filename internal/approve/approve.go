// Package approve asks the user in Telegram to allow an action before den
// takes it, such as a request that writes somewhere other people see.
//
// den asks only for the few actions that reach other people, cost money, or
// cannot be undone. Everything else runs without a question. The user can
// allow one kind of request for good, and den keeps a log of every action
// that changed something outside, asked or not.
package approve

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
)

// Quiet ends what den answers the agent about an action that waits for the
// user. It comes right after the action, where the agent reads it, since a
// reply that only repeats den's question is noise.
const Quiet = "The user already sees den's question with its buttons, so do not mention it. If you have nothing else for them, run den quiet and end your turn."

var (
	ErrDenied  = errors.New("the user did not allow it")
	ErrExpired = errors.New("the user did not answer in time")
	// ErrRestarted is the answer to a question that den stopped before the
	// user answered.
	ErrRestarted = errors.New("den restarted before the user answered; do it again if it is still needed")
)

// Question is an action den asks the user about.
type Question struct {
	// Text is the question in full, such as the request and its body.
	Text string
	// What names the action in one line for the log, such as
	// "crm: POST /api/deals".
	What string
	// Source names where the action goes in a word, such as "Gmail" or
	// "GitHub", for the app's chip.
	Source string
	// Rule names the kind of action, so the user can allow it for good.
	// Empty offers no such choice, for actions that differ each time, such
	// as mail to several people.
	Rule string
	// Label names the rule for the user, such as "crm: PATCH /api/deals/*";
	// empty uses What.
	Label string
	// Scope names a wider kind of action the user can allow for an hour,
	// such as every write to one service but deletes. Empty offers no
	// such choice.
	Scope string
	// ScopeLabel names the scope for the user, such as "every write to
	// Google but deletes"; empty uses What.
	ScopeLabel string
	// File is what Text had no room for, such as a long request body, sent
	// as a file named FileName before the question.
	File     []byte
	FileName string
	// Session is the session whose agent took the action, which the
	// question names when it is not the one the chat is in.
	Session string
}

// MailTo lets the user allow every mail to one address for good or for an
// hour. The question already shows the address, so it says nothing more.
func (q *Question) MailTo(address string) {
	address = strings.ToLower(address)
	q.Rule = "mail to " + address
	q.Label = "Mail to " + address
	q.Scope = q.Rule
	q.ScopeLabel = q.Label
}

// The outcomes of an action in the log.
const (
	Went    = "went"
	Allowed = "allowed by you"
	ByRule  = "allowed by your rule"
	Always  = "allowed for good"
	ForHour = "allowed for an hour"
	ByHour  = "allowed for the hour you gave"
	Denied  = "denied"
	Expired = "expired"
)

// Asker sends questions, routes the answers from the chat back to them,
// and keeps the user's rules and the log of actions.
type Asker struct {
	cfg config.Config
	// Events shows the questions in the app too, which can answer them.
	Events *events.Hub
	// Label names a session above its question in Telegram, as the chat
	// names it above its replies, or is empty for the chat's own session.
	Label func(session string) string
	// Wait is how long a question waits for an answer before den gives
	// up, and Hour how long ForHour allows a question's scope.
	Wait, Hour time.Duration

	mu      sync.Mutex
	pending map[string]*question
	// hours are when each scope the user allowed for an hour ends.
	hours map[string]time.Time
	// later counts the questions asked with Later that still wait.
	later sync.WaitGroup
}

// before says how the user allowed q already, by a rule or for an hour, or
// "" when den has to ask.
func (a *Asker) before(q Question) string {
	if q.Rule != "" && a.allowed(q.Rule) {
		return ByRule
	}
	if q.Scope != "" {
		a.mu.Lock()
		until := a.hours[q.Scope]
		a.mu.Unlock()
		if time.Now().Before(until) {
			return ByHour
		}
	}
	return ""
}

type question struct {
	Question
	answer chan string
	// event is how the app sees the question.
	event events.Event
}

// Pending are the questions that wait for an answer, as the app sees them.
func (a *Asker) Pending() []events.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := make([]events.Event, 0, len(a.pending))
	for _, p := range a.pending {
		list = append(list, p.event)
	}
	return list
}

func New(cfg config.Config) *Asker {
	return &Asker{cfg: cfg, Wait: 15 * time.Minute, Hour: time.Hour, pending: map[string]*question{}, hours: map[string]time.Time{}}
}

// Ask sends the question with Allow and Deny buttons, and Always when it
// names a rule, and waits for the user's answer. It returns nil at once for
// a kind of action the user allowed for good, and when the user allows it.
func (a *Asker) Ask(ctx context.Context, q Question) error {
	if how := a.before(q); how != "" {
		a.Note(q.What, how)
		return nil
	}

	s, err := a.send(ctx, q)
	if err != nil {
		return err
	}
	return s.wait(ctx, a.Wait)
}

// Later asks like Ask without waiting for the answer, for an action den can
// take on its own once the user allows it, so nothing holds a connection
// open in the meantime. It returns false when a rule allows q, and the
// caller goes on at once. Otherwise it sends q, returns true, and calls
// then with the answer, nil when the user allows it, once they give one or
// LaterWait passes. ctx bounds the wait, such as den's own lifetime.
func (a *Asker) Later(ctx context.Context, q Question, then func(error)) (bool, error) {
	if how := a.before(q); how != "" {
		a.Note(q.What, how)
		return false, nil
	}

	s, err := a.send(ctx, q)
	if err != nil {
		return false, err
	}
	a.later.Go(func() { then(s.wait(ctx, LaterWait)) })
	return true, nil
}

// Drain waits until every question asked with Later has its answer. den
// calls it once it stops, when each gets ErrRestarted, so what they tell
// the agent is kept for the next start.
func (a *Asker) Drain() { a.later.Wait() }

// LaterWait is how long a question asked with Later waits for an answer.
// Nothing waits on it but den, so the user can answer when they are back.
var LaterWait = 24 * time.Hour

// sent is a question in the chat, waiting for its answer.
type sent struct {
	a        *Asker
	id       string
	q        *question
	telegram *notify.Telegram
	message  int64
	// label names the session that asked, above the question and what
	// it shrinks to.
	label string
}

func (a *Asker) send(ctx context.Context, q Question) (*sent, error) {
	label := ""
	if a.Label != nil {
		label = a.Label(q.Session)
	}
	text := q.Text
	if label != "" {
		text = label + "\n\n" + text
	}
	// A question cut to fit a message could hide the part the user must
	// see, such as a second branch a push changes.
	if !notify.Fits(text) {
		return nil, errors.New("the question is too long to show the user in one message; send less at once")
	}
	telegram, err := notify.Load(a.cfg)
	if err != nil {
		return nil, fmt.Errorf("asking the user needs Telegram: %w", err)
	}

	// The ID is random, so a button of a question from before a restart
	// cannot answer a new one.
	id := rand.Text()
	buttons := []notify.Button{{Text: "Allow", Data: "ok:" + id}, {Text: "Deny", Data: "no:" + id}}
	// The app answers with these names.
	options := []string{"allow", "deny"}
	if q.Rule != "" {
		buttons = append(buttons, notify.Button{Text: "Always", Data: "al:" + id})
		options = append(options, "always")
	}
	if q.Scope != "" {
		buttons = append(buttons, notify.Button{Text: "1 hour", Data: "hr:" + id})
		options = append(options, "hour")
	}
	p := &question{Question: q, answer: make(chan string, 1),
		event: events.Event{Type: events.Ask, ID: id, Session: q.Session, What: q.What, Source: q.Source, Text: q.Text, Options: options}}
	a.mu.Lock()
	a.pending[id] = p
	a.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, time.Minute)
	var message int64
	// Without the file the user would allow what they cannot read.
	if len(q.File) > 0 {
		err = telegram.SendFile(call, q.FileName, q.File, q.What)
	}
	if err == nil {
		message, err = telegram.SendButtons(call, text, [][]notify.Button{buttons})
	}
	cancel()
	if err != nil {
		a.forget(id)
		return nil, fmt.Errorf("asking the user: %w", err)
	}
	a.Events.Publish(p.event)
	return &sent{a: a, id: id, q: p, telegram: telegram, message: message, label: label}, nil
}

func (a *Asker) forget(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

func (s *sent) wait(ctx context.Context, limit time.Duration) error {
	defer s.a.forget(s.id)

	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case outcome := <-s.q.answer:
		s.a.Note(s.q.What, outcome)
		// Answered in Telegram or the app, the question shrinks in both to
		// its one line and what the answer allowed.
		s.done(s.q.What + "\n" + answered(s.q.Question, outcome))
		if outcome == Denied {
			return ErrDenied
		}
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}

	// Nobody answered: take the buttons away, so a late press does nothing,
	// and shrink the question to its one line. When the asker went away
	// first, such as a command whose agent's turn ended, it stopped rather
	// than ran out of time.
	s.a.Note(s.q.What, Expired)
	result := "\n✗ Expired"
	if ctx.Err() != nil {
		result = "\n✗ Stopped before you answered"
	}
	s.done(s.q.What + result)
	if ctx.Err() != nil {
		return ErrRestarted
	}
	return ErrExpired
}

// done shrinks the question to text, which also takes its buttons away.
func (s *sent) done(text string) {
	s.a.Events.Publish(events.Event{Type: events.Answered, ID: s.id, Text: text})
	if s.label != "" {
		text = s.label + "\n\n" + text
	}
	call, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.telegram.Edit(call, s.message, text); err != nil {
		log.Printf("approve: %v", err)
	}
}

// Answer passes the user's answer to question id: Allowed, Denied, Always,
// which also allows the question's kind of action for good, or ForHour,
// which allows its scope for an hour. It returns what the answered message
// shrinks to: the question's one line and the answer, naming what Always
// and 1 hour allow. It returns false when the question is no longer
// waiting.
func (a *Asker) Answer(id, outcome string) (string, bool) {
	a.mu.Lock()
	p, found := a.pending[id]
	if found {
		delete(a.pending, id)
	}
	a.mu.Unlock()
	if !found {
		return "", false
	}

	if outcome == ForHour {
		if p.Scope == "" {
			outcome = Allowed
		} else {
			a.mu.Lock()
			a.hours[p.Scope] = time.Now().Add(a.Hour)
			a.mu.Unlock()
		}
	}
	if outcome == Always {
		if p.Rule == "" {
			outcome = Allowed
		} else if err := a.allow(p.Rule, cmp.Or(p.Label, p.What)); err != nil {
			log.Printf("approve: %v", err)
		}
	}
	p.answer <- outcome

	// Questions already waiting that the answer covers get it too, as they
	// would have if they had been asked after it. Their waits shrink them in
	// the chat and the app.
	a.mu.Lock()
	var covered []*question
	for id, other := range a.pending {
		if outcome == ForHour && other.Scope == p.Scope || outcome == Always && other.Rule == p.Rule {
			delete(a.pending, id)
			covered = append(covered, other)
		}
	}
	a.mu.Unlock()
	for _, other := range covered {
		if outcome == ForHour {
			other.answer <- ByHour
		} else {
			other.answer <- ByRule
		}
	}
	return p.What + "\n" + answered(p.Question, outcome), true
}

// answered says what the user's answer to q allowed, without repeating
// q's one line.
func answered(q Question, outcome string) string {
	what := ""
	switch outcome {
	case Denied:
		return "✗ Denied"
	case Always:
		what = "✓ Always: " + cmp.Or(q.Label, q.What)
	case ForHour:
		what = "✓ For an hour: " + cmp.Or(q.ScopeLabel, q.What)
	default:
		return "✓ Allowed"
	}
	if strings.HasSuffix(what, ": "+q.What) {
		what, _, _ = strings.Cut(what, ":")
	}
	return what
}

// Done ends what den tells the agent after an action the user allowed went
// through. den tells a session's reports together, so the agent's reply is
// the task's one result.
const Done = "When nothing is left to do, reply with the result in a line or two, not the steps."

// Quote marks text from outside den, such as a service's answer, inside
// what den tells the agent: between two lines with a tag the text cannot
// know, so the text cannot pass itself off as den's own words.
func Quote(text string) string {
	tag := rand.Text()[:8]
	return "<quoted " + tag + ">\n" + text + "\n</quoted " + tag + ">"
}
