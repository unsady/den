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
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/notify"
)

// Wait is how long a question waits for an answer before den gives up.
var Wait = 15 * time.Minute

var (
	ErrDenied  = errors.New("the user did not allow it")
	ErrExpired = errors.New("the user did not answer in time")
)

// Question is an action den asks the user about.
type Question struct {
	// Text is the question in full, such as the request and its body.
	Text string
	// What names the action in one line for the log, such as
	// "crm: POST /api/deals".
	What string
	// Rule names the kind of action, so the user can allow it for good.
	// Empty offers no such choice, for actions that differ each time, such
	// as mail.
	Rule string
	// Label names the rule for the user, such as "crm: PATCH /api/deals/*";
	// empty uses What.
	Label string
}

// The outcomes of an action in the log.
const (
	Went    = "went"
	Allowed = "allowed by you"
	ByRule  = "allowed by your rule"
	Always  = "allowed for good"
	Denied  = "denied"
	Expired = "expired"
)

// Asker sends questions, routes the answers from the chat back to them,
// and keeps the user's rules and the log of actions.
type Asker struct {
	cfg config.Config

	mu      sync.Mutex
	next    int
	pending map[string]*question
}

type question struct {
	Question
	answer chan string
}

func New(cfg config.Config) *Asker {
	return &Asker{cfg: cfg, pending: map[string]*question{}}
}

// Ask sends the question with Allow and Deny buttons, and Always when it
// names a rule, and waits for the user's answer. It returns nil at once for
// a kind of action the user allowed for good, and when the user allows it.
func (a *Asker) Ask(ctx context.Context, q Question) error {
	if q.Rule != "" && a.allowed(q.Rule) {
		a.Note(q.What, ByRule)
		return nil
	}

	telegram, err := notify.Load(a.cfg)
	if err != nil {
		return fmt.Errorf("asking the user needs Telegram: %w", err)
	}

	a.mu.Lock()
	a.next++
	id := strconv.Itoa(a.next)
	p := &question{Question: q, answer: make(chan string, 1)}
	a.pending[id] = p
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}()

	buttons := []notify.Button{{Text: "Allow", Data: "ok:" + id}, {Text: "Deny", Data: "no:" + id}}
	if q.Rule != "" {
		buttons = append(buttons, notify.Button{Text: "Always", Data: "al:" + id})
	}
	call, cancel := context.WithTimeout(ctx, time.Minute)
	message, err := telegram.SendButtons(call, q.Text, [][]notify.Button{buttons})
	cancel()
	if err != nil {
		return fmt.Errorf("asking the user: %w", err)
	}

	timer := time.NewTimer(Wait)
	defer timer.Stop()
	select {
	case outcome := <-p.answer:
		a.Note(q.What, outcome)
		if outcome == Denied {
			return ErrDenied
		}
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}

	// Nobody answered: take the buttons away, so a late press does nothing.
	a.Note(q.What, Expired)
	call, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := telegram.Edit(call, message, q.Text+"\n\n✗ Expired"); err != nil {
		log.Printf("approve: %v", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrExpired
}

// Answer passes the user's answer to question id: Allowed, Denied, or
// Always, which also allows the question's kind of action for good. It
// returns the question's text, or false when it is no longer waiting.
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

	if outcome == Always {
		if p.Rule == "" {
			outcome = Allowed
		} else if err := a.allow(p.Rule, cmp.Or(p.Label, p.What)); err != nil {
			log.Printf("approve: %v", err)
		}
	}
	p.answer <- outcome
	return p.Text, true
}
