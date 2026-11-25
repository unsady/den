// Package chat lets the user talk to the agent through the Telegram bot.
//
// Messages from the configured chat become turns of one fx session, run one
// at a time by fx acp, which stays up between turns. A message that arrives
// during a turn joins it between two of its steps; one for another session,
// or with a photo, waits and goes with the others as the next turn. Messages
// from any other chat are ignored.
//
// Photos, voice messages, and files are saved in the inbox as the agent, and
// the turn names their paths. A photo is also attached to the turn as an
// image, and a voice message comes with its transcript.
package chat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
	"github.com/unsady/den/internal/usage"
)

const (
	// wait is how long one getUpdates call waits for a message, in seconds.
	wait = 50
	// retry is the pause after an error or while Telegram is not configured.
	retry = 15 * time.Second
	// typing renews Telegram's typing status, which lasts about five seconds.
	typing = 4 * time.Second
	// steps is how many of the latest steps the progress message shows.
	steps = 5
)

// progressEvery is how often the progress message is updated. A turn that
// ends sooner shows no progress message. Tests shorten it.
var progressEvery = 3 * time.Second

// commands fill the bot's command menu and the help message.
var commands = []notify.Command{
	{Name: "new", Description: "Start a new session"},
	{Name: "sessions", Description: "Switch to a recent session, or find one: /sessions <words>"},
	{Name: "model", Description: "Choose the model and provider"},
	{Name: "effort", Description: "Choose how hard the model thinks"},
	{Name: "bg", Description: "Run a task in the background, in a session of its own: /bg <task>"},
	{Name: "stop", Description: "Stop the current turn"},
	{Name: "compact", Description: "Shrink the session to a summary"},
	{Name: "jobs", Description: "List the jobs"},
	{Name: "log", Description: "What the agent did outside today, and what you always allow"},
	{Name: "status", Description: "Show the model, context, memory, and jobs"},
	{Name: "usage", Description: "Show the tokens den used"},
	{Name: "bug", Description: "Report a bug or suggest an idea"},
	{Name: "help", Description: "Show what the bot does"},
}

// bugReport answers /bug.
const bugReport = "Found a bug or have an idea? Open an issue on GitHub:\nhttps://github.com/unsady/den/issues/new\n\nOr write to den@bazutya.com.\n\n" +
	"Say what you did, what den answered, and what you expected. The output of den version on your computer helps too."

// recentSessions is how many sessions /sessions offers.
const recentSessions = 8

// Daemon is what the chat needs from the rest of den: its jobs, and the
// questions den asked the user.
type Daemon interface {
	ListJobs() []jobs.Job
	// Answer passes the user's answer to question id, one of approve's
	// outcomes, and returns what the answered message shrinks to, or false
	// when it is no longer waiting.
	Answer(id, outcome string) (string, bool)
	// Rules, Forget, and Actions are the kinds of action the user allowed
	// for good and the log of actions, for /log.
	Rules() []approve.Rule
	Forget(id string) (approve.Rule, bool)
	Actions(since time.Time) []approve.Action
}

func help() string {
	text := "Write to talk to the agent, such as:\n" +
		"\n• Every weekday at 9, send me the weather for the day." +
		"\n• What can you do, and what can I connect?" +
		"\n• Help me connect my Todoist.\n"
	for _, c := range commands {
		text += "\n/" + c.Name + ": " + c.Description
	}
	return text
}

// entry is one message waiting for the next turn.
type entry struct {
	text string
	// image is the saved path of a photo to attach.
	image string
	// report marks den's report of what came of an action, and notice is
	// what den tells the user instead when the agent does not get it, after
	// /stop or a switch; nothing when it is empty.
	report bool
	notice string
	// session is where the message goes: the chat's session when it came,
	// or another, such as a job's whose message it replies to.
	session string
	// shown is the message as the user wrote it, when text adds den's
	// notes for the agent, such as a file's path or the quoted message.
	shown string
	// file is the file the user sent, which den keeps for the app beside
	// the agent's copy in the inbox; shown is then only its caption.
	file *Upload
}

type Chat struct {
	cfg   config.Config
	den   Daemon
	usage *usage.Store
	// store keeps the sessions, each client's cursor, and which session
	// wrote each of the bot's messages.
	store *store.Store
	// Events tells the app what the chat does.
	Events *events.Hub

	mu   sync.Mutex
	stop context.CancelFunc
	// menu is set once the command menu was sent to Telegram.
	menu bool
	wake chan struct{}
	// squeeze asks the worker to compact the session now, for /compact.
	squeeze chan struct{}
	// notes carry what a running command says about itself, such as how
	// many items den map has done, to the progress message.
	notes chan string
	// attached are the files each running turn sent, by its session,
	// which go with its reply.
	attached map[string][]Upload
	// bg stops each turn that /bg runs, by its ID, while it runs, and
	// background waits for them to end.
	bg         map[string]context.CancelFunc
	background sync.WaitGroup
	// turns stop the turn that runs in each session, the chat's or a /bg
	// task's, by its session, for a stop of one session.
	turns map[string]context.CancelFunc
	// albums hold the files of an album until the last one is saved.
	albums map[string]*album

	// agent runs the turns in the session a client is in. steering holds
	// the agent of each running turn, the chat's or one of its own, by its
	// session, so a message for that session joins the turn.
	agent    *runner.Agent
	steering map[string]*runner.Agent
	// steered are the messages passed to the running turn, by the key
	// passed with them, which come back as a turn of their own if fx did
	// not deliver them, unless /stop dropped them.
	steered map[string]steered
	// steps holds the latest step of each session's running turn, empty
	// until its first, so a client that reconnects reads what runs.
	steps map[string]string
	// busy counts the turns that run in each session, the chat's, a job's
	// or a /bg task's, and compacting, whose messages wait until they end.
	busy map[string]int
	// stopping is set once den is stopping: no turn, /bg task or
	// compacting starts, and messages wait in the inbox for the next start.
	stopping bool
	// life is Run's context, which ends when den stops; a /bg task runs
	// under it whoever started it.
	life context.Context
	// ended says a turn on the chat's agent ended, from when the user's
	// time away counts.
	ended chan struct{}
	// titled are the sessions den asked a title for since it started.
	titled map[string]bool
	// quiet holds when each session's agent said it had nothing to send,
	// until the turn that said it ends.
	quiet map[string]time.Time
	// remembered holds the lines each session's agent left on what it
	// changed in its memory, until the turn that left them ends.
	remembered map[string]remembered
}

// Quiet marks the running turn in session as one with nothing to send.
// Like Tell, it trusts the session the agent names: the mark only decides
// whether the turn's reply is delivered.
func (c *Chat) Quiet(session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quiet[session] = time.Now()
}

// Quieted says whether the turn in session that started at since was
// marked quiet, and clears the mark. A mark from before since belongs to an
// earlier turn and does not count.
func (c *Chat) Quieted(session string, since time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.quiet[session]
	delete(c.quiet, session)
	return ok && !at.Before(since)
}

// Tell gives the agent text as a new turn in session, such as what came of
// a command that waited for the user after the agent's turn ended. It
// returns false when den does not have session, such as one the user
// deleted meanwhile, and the caller tells the user instead.
func (c *Chat) Tell(session, text, notice string) bool {
	if !c.Known(session) {
		return false
	}
	if err := c.enqueue(entry{text: text, report: true, notice: notice, session: session}); err != nil {
		log.Printf("chat: report: %v", err)
		return false
	}
	return true
}

// Known says whether den has session.
func (c *Chat) Known(session string) bool {
	if session == "" {
		return false
	}
	_, err := c.store.Session(context.Background(), session)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("chat: session %s: %v", session, err)
	}
	return err == nil
}

// Drain stops the chat from starting turns, /bg tasks and compacting, as
// den does before it stops. Messages that come meanwhile wait in the inbox
// for den's next start, and the turns that run go on.
func (c *Chat) Drain() {
	c.mu.Lock()
	c.stopping = true
	c.mu.Unlock()
	c.poke()
}

// Idle says whether nothing runs: no turn, job, /bg task or compacting.
func (c *Chat) Idle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.busy) == 0 && c.stop == nil
}

// isStopping says whether Drain was called.
func (c *Chat) isStopping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopping
}

// cutShort tells the user, in the app and in Telegram when its chat is in
// session, that den stopped before session's turn ended.
func (c *Chat) cutShort(session string) {
	const text = "den restarted before this turn ended. Write to go on."
	c.Events.Publish(events.Event{Type: events.Reply, Session: session, Text: text, Failed: true})
	if !c.inTelegram(session) {
		return
	}

	telegram, err := notify.Load(c.cfg)
	if err != nil {
		return
	}
	call, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := text
	if label := c.Label(session); label != "" {
		out = label + "\n\n" + out
	}
	ids, err := telegram.SendIDs(call, out)
	if err != nil {
		log.Printf("chat: send: %v", err)
		return
	}
	c.Sent(ids, session)
}

// Occupy marks session busy while a turn runs in it, such as a job's, so
// its messages wait and it is not deleted, until release.
func (c *Chat) Occupy(session string) (release func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.occupy(session)
}

// claim occupies session unless it is busy already.
func (c *Chat) claim(session string) (release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy[session] > 0 {
		return nil, false
	}
	return c.occupy(session), true
}

// occupy marks session busy until release. The caller holds c.mu.
func (c *Chat) occupy(session string) (release func()) {
	c.busy[session]++

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.busy[session]--; c.busy[session] <= 0 {
				delete(c.busy, session)
			}
			c.mu.Unlock()
			c.poke()
		})
	}
}

// occupied lists the busy sessions. The caller holds c.mu.
func (c *Chat) occupied() []string {
	return slices.Collect(maps.Keys(c.busy))
}

// poke wakes the worker to run what waits in the inbox.
func (c *Chat) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func New(cfg config.Config, den Daemon, usage *usage.Store, st *store.Store) *Chat {
	c := &Chat{
		cfg:        cfg,
		den:        den,
		usage:      usage,
		store:      st,
		wake:       make(chan struct{}, 1),
		squeeze:    make(chan struct{}, 1),
		ended:      make(chan struct{}, 1),
		notes:      make(chan string, 1),
		bg:         map[string]context.CancelFunc{},
		turns:      map[string]context.CancelFunc{},
		attached:   map[string][]Upload{},
		agent:      runner.NewAgent(cfg),
		busy:       map[string]int{},
		steering:   map[string]*runner.Agent{},
		steered:    map[string]steered{},
		titled:     map[string]bool{},
		quiet:      map[string]time.Time{},
		remembered: map[string]remembered{},
		steps:      map[string]string{},
		Events:     &events.Hub{Dir: filepath.Join(cfg.State, "journal")},
	}
	c.agent.OnDropped = c.undelivered
	c.agent.OnTurn = c.steeredTurn

	if c.waiting() {
		c.poke()
	}
	return c
}

// newSession names a new session. Tests count instead.
var newSession = store.NewID

// Run reads the chat and runs its turns until ctx ends and the current turn
// has stopped.
func (c *Chat) Run(ctx context.Context) {
	c.mu.Lock()
	c.life = ctx
	c.mu.Unlock()
	var wg sync.WaitGroup
	wg.Go(func() { c.work(ctx) })
	// The worker starts turns, so it ends before den waits for them.
	defer c.agent.Close()
	defer c.background.Wait()
	defer wg.Wait()

	for ctx.Err() == nil {
		// Secrets are read on each poll, so they can be added after den starts.
		telegram, err := notify.Load(c.cfg)
		if err != nil {
			sleep(ctx, retry)
			continue
		}

		if !c.menu {
			c.menu = c.setMenu(ctx, telegram)
		}

		poll, cancel := context.WithTimeout(ctx, (wait+20)*time.Second)
		messages, next, strangers, err := telegram.Updates(poll, c.offset(), wait)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("chat: %v", err)
				sleep(ctx, retry)
			}
			continue
		}

		for _, s := range strangers {
			log.Printf("chat: ignored a message from %s", s)
		}
		c.setOffset(next)

		for _, m := range messages {
			c.receive(ctx, telegram, m)
		}
	}
}

// Forget deletes session: drop deletes what fx keeps of it, then the chat
// forgets it. Telegram's chat, or the app, starts a new one with its next
// message when it was in it. It refuses while a turn runs in the session,
// which the user can /stop, a job or a /bg task runs in it, or den compacts
// it.
func (c *Chat) Forget(session string, drop func() error) error {
	c.mu.Lock()
	running := c.busy[session] > 0
	c.mu.Unlock()
	if running {
		return fmt.Errorf("%w; send /stop first", ErrBusy)
	}

	if drop != nil {
		if err := drop(); err != nil {
			return err
		}
	}
	if err := c.store.DeleteSession(context.Background(), session); err != nil {
		return err
	}
	c.Events.Forget(session)
	c.Events.Publish(events.Event{Type: events.Deleted, Session: session})
	return nil
}

// ErrBusy is Forget's error while the chat works in the session.
var ErrBusy = errors.New("the chat is working in this session")

// ago says how long before now t was, such as "5m ago" or "3d ago".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func clipRunes(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return text
}

func lastLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	return text
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// steered is a message passed to the running turn: its text, the session
// it went to, and when.
type steered struct {
	text    string
	session string
	at      time.Time
}
