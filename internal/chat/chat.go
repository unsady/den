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
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/acp"
	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/sessions"
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
	// ContextSize is the largest prompt sent to the model since a time, in
	// tokens, or 0 when unknown.
	ContextSize(since time.Time) int
	// LastContext is the size of the last prompt since a time, in tokens,
	// or 0 when unknown.
	LastContext(since time.Time) int
	// Busy says whether work that a chat turn started, such as a den map
	// run, still goes on in den's background.
	Busy(turn string) bool
}

func help() string {
	text := "Write to talk to the agent. Messages sent while it works go together as the next turn.\n"
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
	// app marks a message from the app, whose turn answers only there.
	app bool
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
	// turnID names the running turn to the commands its agent runs, and
	// is empty between turns.
	turnID string
	// notes carry what a running command says about itself, such as how
	// many items den map has done, to the progress message.
	notes chan string
	// attached are the files the running turn sent, which go with its
	// reply.
	attached []Upload
	// turns are the latest turns, so den can tell the agent of the session
	// a turn ran in what came of a command it started.
	turns []turnRef
	// bg stops each turn that /bg runs, by its ID, while it runs, and
	// background waits for them to end.
	bg         map[string]context.CancelFunc
	background sync.WaitGroup
	// albums hold the files of an album until the last one is saved.
	albums map[string]*album

	// agent runs the turns in chat sessions. steering is the session of the
	// running turn when it is one of them, which a message for that session
	// can join, and agentTurn is the latest of them, which the agent's
	// DEN_TURN stands for.
	agent     *runner.Agent
	steering  string
	agentTurn string
	// steered are the messages passed to the running turn, by the session
	// they went to, which come back as a turn of their own if fx did not
	// deliver them, unless /stop dropped them.
	steered map[string]string
	// compacting is the session /compact or idle compaction works on.
	compacting string
	// titled are the sessions den asked a title for since it started.
	titled map[string]bool
}

type turnRef struct {
	id string
	// session is the turn's.
	session string
	// bg marks a /bg turn, whose session is known only once it ends;
	// reports for it wait in pending until then.
	bg      bool
	pending []entry
	// held are the notices the turn sent the user while its session was
	// unknown, which go to the app once it is.
	held []string
	// app marks a turn that answers only in the app.
	app bool
}

// keptTurns is how many of the latest turns Tell recognizes.
const keptTurns = 100

// Current is the running turn's ID, or "" between turns. While /bg turns
// run besides it, den cannot tell which agent asked, so it is "" and what
// came of the action goes to the user.
func (c *Chat) Current() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case len(c.bg) == 0:
		return c.turnID
	case c.turnID == "" && len(c.bg) == 1:
		for id := range c.bg {
			return id
		}
	}
	return ""
}

// Tell gives the agent text as a new turn in the session of turn, such as
// what came of a command that waited for the user after the agent's turn
// ended. It returns false when the chat does not know turn, such as a
// job's, and the caller tells the user instead.
func (c *Chat) Tell(turn, text, notice string) bool {
	c.mu.Lock()
	turn = c.resolve(turn)
	var ready *entry
	found := false
	for i, t := range c.turns {
		if t.id != turn {
			continue
		}
		e := entry{text: text, report: true, notice: notice, session: t.session}
		found = true
		if t.bg && t.session == "" {
			c.turns[i].pending = append(t.pending, e)
		} else {
			ready = &e
		}
		break
	}
	c.mu.Unlock()
	if ready != nil {
		if err := c.enqueue(*ready); err != nil {
			log.Printf("chat: report: %v", err)
			return false
		}
	}
	return found
}

// Notify says which session a notice from turn, the agent's DEN_TURN,
// belongs in. A /bg turn knows its session only when it ends, so the
// notice waits until then, and held says so. A turn the chat does not
// know gives no session.
func (c *Chat) Notify(turn, text string) (session string, held bool) {
	if turn == "" {
		return "", false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	turn = c.resolve(turn)
	for i, t := range c.turns {
		if t.id != turn {
			continue
		}
		if t.bg && c.bg[t.id] != nil {
			c.turns[i].held = append(t.held, text)
			return "", true
		}
		return t.session, false
	}
	return "", false
}

// resolve returns the turn the agent's DEN_TURN stands for, its latest, and
// any other turn as it is. The caller holds c.mu.
func (c *Chat) resolve(turn string) string {
	if turn == c.agent.Key() {
		return c.agentTurn
	}
	return turn
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
		cfg:     cfg,
		den:     den,
		usage:   usage,
		store:   st,
		wake:    make(chan struct{}, 1),
		squeeze: make(chan struct{}, 1),
		notes:   make(chan string, 1),
		bg:      map[string]context.CancelFunc{},
		agent:   runner.NewAgent(cfg),
		steered: map[string]string{},
		titled:  map[string]bool{},
		Events:  &events.Hub{Dir: filepath.Join(cfg.State, "journal")},
	}
	c.agent.OnDropped = c.undelivered
	c.agent.OnTurn = c.steeredTurn

	if c.waiting() {
		c.poke()
	}
	return c
}

// newSession names a new session as fx names one: 9 random bytes in
// URL-safe base64, not starting with '-'. Tests count instead.
var newSession = func() string {
	b := make([]byte, 9)
	for {
		rand.Read(b)
		if id := base64.RawURLEncoding.EncodeToString(b); id[0] != '-' {
			return id
		}
	}
}

// Run reads the chat and runs its turns until ctx ends and the current turn
// has stopped.
func (c *Chat) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { c.work(ctx) })
	defer c.agent.Close()
	defer wg.Wait()
	defer c.background.Wait()

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

func (c *Chat) receive(ctx context.Context, telegram *notify.Telegram, m notify.Message) {
	if m.Callback != nil {
		c.press(ctx, telegram, *m.Callback)
		return
	}
	if m.Other {
		c.send(ctx, telegram, "den reads text, photos, voice messages, and files for now.")
		return
	}
	if m.File != nil {
		c.receiveFile(ctx, telegram, m)
		return
	}

	if task, ok := bgTask(m.Text); ok {
		c.runBackground(ctx, telegram, task)
		return
	}

	name, arg := command(m.Text)
	if arg != "" && name != "/model" && name != "/effort" && name != "/sessions" {
		name = ""
	}
	switch name {
	case "/start", "/help":
		c.send(ctx, telegram, help())

	case "/new":
		c.switchTo("")
		text := "✦ New session"
		if model := c.modelName(ctx); model != "" {
			text += " · " + model
		}
		c.sendHTML(ctx, telegram, text, nil)

	case "/model":
		if arg == "" {
			c.recentModels(ctx, telegram, 0)
		} else {
			c.findModel(ctx, telegram, arg)
		}

	case "/effort":
		if arg == "" {
			c.listEfforts(ctx, telegram)
		} else {
			text, err := c.chooseEffort(ctx, arg)
			c.reply(ctx, telegram, text, err)
		}

	case "/sessions":
		c.listSessions(ctx, telegram, arg, false, 0)

	case "/jobs":
		c.listJobs(ctx, telegram, 0)

	case "/log":
		c.showLog(ctx, telegram, 0)

	case "/status":
		c.sendHTML(ctx, telegram, c.status(ctx), nil)

	case "/bug":
		c.send(ctx, telegram, bugReport)

	case "/usage":
		c.send(ctx, telegram, c.usage.Report(time.Now()))

	case "/compact":
		c.mu.Lock()
		busy := c.stop != nil
		c.mu.Unlock()
		select {
		case c.squeeze <- struct{}{}:
			if busy {
				c.send(ctx, telegram, "den compacts the session after this turn.")
			}
		default:
		}

	case "/stop":
		stopped, notices := c.halt()
		if stopped {
			c.send(ctx, telegram, "Stopped.")
		} else {
			c.send(ctx, telegram, "Nothing is running.")
		}
		c.tell(ctx, telegram, notices)

	default:
		c.queue(ctx, telegram, m, entry{text: quoted(m, m.Text), shown: m.Text})
	}
}

// halt stops the running turns, /bg's too, and drops what waits for the
// next turn. It reports whether anything ran, and returns what den's
// dropped reports would have told the agent, for the user.
func (c *Chat) halt() (bool, []string) {
	var notices []string
	dropped, err := c.store.ClearInbox(context.Background())
	if err != nil {
		log.Printf("chat: inbox: %v", err)
	}
	for _, m := range dropped {
		if m.Report && m.Notice != "" {
			notices = append(notices, m.Notice)
		}
	}

	c.mu.Lock()
	stop := c.stop
	clear(c.steered)
	var stops []context.CancelFunc
	for _, s := range c.bg {
		stops = append(stops, s)
	}
	c.mu.Unlock()
	if stop != nil {
		stops = append(stops, stop)
	}
	for _, s := range stops {
		s()
	}
	return len(stops) > 0, notices
}

// quoted adds the part of an earlier message that m replies to after text.
// It carries only a few hundred characters: enough to say which message is
// meant, since the bot's own replies are already in the session.
func quoted(m notify.Message, text string) string {
	if m.Quote == "" {
		return text
	}
	who := "the user's earlier message"
	if m.QuotesBot {
		who = "your earlier message"
	}
	return text + "\n[In reply to " + who + ": «" + m.Quote + "»]"
}

// switchTo puts Telegram's chat in session, or makes its next message start
// a new one when session is empty. Turns that run in the session it leaves
// go on there.
func (c *Chat) switchTo(session string) {
	if err := c.enter(store.Telegram, session); err != nil {
		log.Printf("chat: switch: %v", err)
	}
	c.Events.Publish(events.Event{Type: events.Settings, Session: session})
}

// Forget deletes session: drop deletes what fx keeps of it, then the chat
// forgets it. Telegram's chat, or the app, starts a new one with its next
// message when it was in it. It refuses while a turn runs in the session,
// which the user can /stop, or den compacts it.
func (c *Chat) Forget(session string, drop func() error) error {
	c.mu.Lock()
	running := c.compacting == session ||
		c.stop != nil && slices.ContainsFunc(c.turns, func(t turnRef) bool { return t.id == c.turnID && t.session == session })
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
	return nil
}

// ErrBusy is Forget's error while the chat works in the session.
var ErrBusy = errors.New("the chat is working in this session")

// tell sends the user den's reports their agent did not get.
// Without telegram, as for the app, only the app hears them.
func (c *Chat) tell(ctx context.Context, telegram *notify.Telegram, notices []string) {
	for _, n := range notices {
		c.Events.Publish(events.Event{Type: events.Notice, Text: n})
		if telegram != nil {
			c.send(ctx, telegram, n)
		}
	}
}

// queue keeps a message for the next turn. While a turn runs, it marks the
// message with 👀, so the user sees it waits.
//
// A text for the chat's own session joins the running turn instead, when it
// is one of the agent's, and is marked with ✍.
func (c *Chat) queue(ctx context.Context, telegram *notify.Telegram, m notify.Message, entries ...entry) {
	id := m.ID
	session, err := c.target(m.ReplyTo)
	if err != nil {
		log.Printf("chat: queue: %v", err)
		c.send(ctx, telegram, "den could not keep the message: "+err.Error())
		return
	}
	for i := range entries {
		entries[i].session = session
	}

	c.published(entries)
	if c.steer(ctx, entries) {
		c.react(ctx, telegram, id, "✍")
		return
	}

	if err := c.enqueue(entries...); err != nil {
		log.Printf("chat: queue: %v", err)
		c.send(ctx, telegram, "den could not keep the message: "+err.Error())
		return
	}
	c.mu.Lock()
	busy := c.stop != nil
	c.mu.Unlock()

	if busy {
		c.react(ctx, telegram, id, "👀")
	}
}

// steer passes the texts of entries to the running turn, when it is the
// agent's and they are for its session. It reports false when the caller
// keeps them for the next turn instead.
func (c *Chat) steer(ctx context.Context, entries []entry) bool {
	c.mu.Lock()
	session := c.steering
	ok := session != ""
	var texts []string
	for _, e := range entries {
		ok = ok && e.session == session && e.image == "" && !e.report
		texts = append(texts, e.text)
	}
	c.mu.Unlock()
	if !ok || len(texts) == 0 {
		return false
	}

	text := strings.Join(texts, "\n\n")
	c.mu.Lock()
	c.steered[text] = session
	c.mu.Unlock()
	if err := c.agent.Steer(ctx, text); err != nil {
		c.mu.Lock()
		delete(c.steered, text)
		c.mu.Unlock()
		if !errors.Is(err, acp.ErrIdle) {
			log.Printf("chat: steer: %v", err)
		}
		return false
	}
	return true
}

// undelivered runs a message passed to a turn that fx did not deliver as a
// turn of its own in its session, unless /stop dropped it meanwhile.
func (c *Chat) undelivered(text string) {
	c.mu.Lock()
	session, kept := c.steered[text]
	delete(c.steered, text)
	c.mu.Unlock()
	if !kept {
		return
	}
	if err := c.enqueue(entry{text: text, session: session}); err != nil {
		log.Printf("chat: undelivered message: %v", err)
	}
}

// steeredTurn sends the reply to a message passed to a turn that fx ran as a
// turn of its own, since the turn it was meant for had just ended.
func (c *Chat) steeredTurn(text string, reply runner.Reply) {
	c.mu.Lock()
	delete(c.steered, text)
	c.mu.Unlock()
	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	if reply.Error == "" && runner.IsSilent(reply.Output) {
		return
	}
	c.Events.Publish(events.Event{Type: events.Reply, Session: reply.SessionID, Text: answer(reply, true), Failed: reply.Error != ""})

	telegram, err := notify.Load(c.cfg)
	if err != nil {
		return
	}
	call, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out := answer(reply, true)
	if label := c.label(reply.SessionID); label != "" {
		out = label + "\n\n" + out
	}
	ids, err := telegram.SendIDs(call, out)
	if err != nil {
		log.Printf("chat: send: %v", err)
	}
	c.Sent(ids, reply.SessionID)
}

// react marks the user's message id, unless it is 0.
func (c *Chat) react(ctx context.Context, telegram *notify.Telegram, id int64, emoji string) {
	if id == 0 {
		return
	}
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.React(call, id, emoji); err != nil {
		log.Printf("chat: react: %v", err)
	}
}

// work runs the waiting messages as turns, one turn at a time, and
// compacts a long session once the user has been away for a while.
func (c *Chat) work(ctx context.Context) {
	var idle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-idle:
			idle = nil
			c.compact(ctx)
		case <-c.squeeze:
			if telegram, err := notify.Load(c.cfg); err == nil {
				c.send(ctx, telegram, c.compactNow(ctx))
			}
		}

		ran := false
		for ctx.Err() == nil && c.turn(ctx) {
			ran = true
		}
		if ran {
			idle = time.After(compactIdle)
		}
	}
}

// turn runs the waiting messages for one session as one turn. It returns
// false when there were none.
func (c *Chat) turn(ctx context.Context) bool {
	// Messages for another session, such as replies to a job's message,
	// wait for a turn of their own. The session is read before its
	// messages leave the inbox, so a failure leaves them for the next try.
	session, err := c.store.NextTurn(context.Background())
	if err != nil || session == "" {
		if err != nil {
			log.Printf("chat: inbox: %v", err)
		}
		return false
	}
	choice, agent, err := c.choiceOf(session)
	if err != nil {
		log.Printf("chat: session %s: %v", session, err)
		return false
	}
	messages, err := c.store.TakeTurn(context.Background(), session)
	if err != nil || len(messages) == 0 {
		if err != nil {
			log.Printf("chat: inbox: %v", err)
		}
		return false
	}
	untitled := c.untitled(session)

	var texts, images []string
	// A turn started by den's reports alone, such as that a den map run
	// ended, may end without a word to the user.
	// A turn of the app's messages alone answers only in the app; den's
	// reports go with the messages around them.
	reports, app, asked := true, true, false
	for _, m := range messages {
		reports = reports && m.Report
		if !m.Report {
			asked = true
			app = app && m.App
		}
		texts = append(texts, m.Text)
		if m.Image != "" {
			images = append(images, m.Image)
		}
	}
	app = app && asked
	prompt := strings.Join(texts, "\n\n")

	c.mu.Lock()
	provider, model, effort := choice.Provider, choice.Model, choice.Effort
	turn, stop := context.WithCancel(ctx)
	c.stop = stop
	current := rand.Text()
	c.turnID = current
	c.attached = nil
	c.turns = append(c.turns, turnRef{id: current, session: session, app: app})
	// A turn in a chat session runs on the agent, which starts a session
	// den named before its first turn; one in a job's or a /bg task's
	// session runs once with fx ask, on the model that ran it.
	if agent {
		c.steering = session
		c.agentTurn = current
	}
	if len(c.turns) > keptTurns {
		c.turns = c.turns[len(c.turns)-keptTurns:]
	}
	c.mu.Unlock()
	defer stop()
	started := time.Now()
	// A chat session gets its title from the user's first message, while
	// its first turn runs.
	if untitled && asked {
		go c.title(session, prompt)
	}

	done := make(chan struct{})
	shown := make(chan struct{})
	steps := make(chan string, 64)
	if !app {
		go c.showTyping(turn, done)
	}
	go func() {
		c.showProgress(ctx, steps, done, !app)
		close(shown)
	}()
	req := runner.Request{
		Prompt:     prompt,
		Provider:   provider,
		Model:      model,
		Effort:     effort,
		Resume:     session,
		Images:     images,
		Turn:       current,
		MaySilence: true,
		Progress: func(line string) {
			if text, ok := step(line); ok {
				c.Events.Publish(events.Event{Type: events.Step, Session: session, Text: text})
			}
			select {
			case steps <- line:
			default:
			}
		},
	}
	var reply runner.Reply
	if agent {
		reply = c.agent.Ask(turn, req)
	} else {
		reply = runner.Ask(turn, c.cfg, req)
	}
	close(done)
	<-shown

	c.mu.Lock()
	c.stop = nil
	c.turnID = ""
	files := c.attached
	c.attached = nil
	c.steering = ""
	c.mu.Unlock()

	if err := c.store.Touch(context.Background(), session); err != nil {
		log.Printf("chat: touch %s: %v", session, err)
	}
	// The agent's turns tell how big their session has grown, and the
	// first reply after den compacted a session says so.
	compacted := ""
	if agent {
		compacted = c.measure(session, started, reply.Error == "")
	}

	log.Printf("chat: turn in %s", reply.Duration)
	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	// The app hears the turn end however it ends.
	defer c.Events.Publish(events.Event{Type: events.Idle, Session: session})
	if turn.Err() == context.Canceled {
		// /stop already answered, or den is shutting down.
		return true
	}
	// A turn may end without a word, such as one den's report started, or
	// one that answers a plain "ok". When a subagent's report took the
	// last word, the answer before it is in the session. Files the turn
	// sent go without a word.
	silent := false
	if reply.Error == "" && runner.IsSilent(reply.Output) {
		earlier := ""
		if !reports && (c.den == nil || !c.den.Busy(current) && !c.den.Busy(c.agent.Key())) {
			earlier = sessions.Answer(c.cfg.Sessions, session, started)
		}
		switch {
		case (earlier == "" || runner.IsSilent(earlier)) && len(files) == 0:
			log.Printf("chat: the turn had nothing to tell")
			return true
		case earlier == "" || runner.IsSilent(earlier):
			silent = true
		default:
			reply.Output = earlier
		}
	}

	text := ""
	if !silent {
		text = answer(reply, !reply.New)
	}
	// The app shows each session apart, so its reply needs no label.
	c.Events.Publish(events.Event{Type: events.Reply, Session: session, Text: text, Failed: reply.Error != "", Files: c.keep(session, files)})
	// A reply from a session other than the chat's, such as a reply to a
	// job's message or a turn the chat left with /new, says which.
	if label := c.label(session); label != "" {
		text = label + "\n\n" + text
	}
	text = compacted + text
	if app {
		return true
	}

	if telegram, err := notify.Load(c.cfg); err == nil {
		call, cancel := context.WithTimeout(ctx, time.Minute)
		ids, err := telegram.SendIDs(call, text)
		cancel()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
		if len(files) > 0 {
			call, cancel := context.WithTimeout(ctx, 5*time.Minute)
			sent, err := telegram.SendFiles(call, uploads(files))
			cancel()
			if err != nil {
				log.Printf("chat: send files: %v", err)
			}
			ids = append(ids, sent...)
		}
		c.Sent(ids, session)
	}
	return true
}

// measure records session's size after a turn that started then, and
// returns the line that tells the user den compacted it while they were
// away, once, after a turn that did not fail.
func (c *Chat) measure(session string, started time.Time, ok bool) string {
	ctx := context.Background()
	size := c.contextSize(started)
	if size > 0 {
		if err := c.store.SetContext(ctx, session, size); err != nil {
			log.Printf("chat: context of %s: %v", session, err)
		}
	}
	s, err := c.store.Session(ctx, session)
	if err != nil || s.Compacted == 0 || !ok {
		return ""
	}
	if err := c.store.SetCompacted(ctx, session, 0); err != nil {
		log.Printf("chat: compacted %s: %v", session, err)
	}
	line := "🗜 While you were away, den compacted this session"
	if s.Context > 0 {
		line += fmt.Sprintf(": %s → %s tokens", tokens(s.Compacted), tokens(s.Context))
	}
	return line + ".\n\n"
}

// fxErrors say the errors fx names that the user can act on.
var fxErrors = map[string]string{
	"SubscriptionNativeImageUnavailable": "This model cannot see images. Pick another with /model and send the image again.",
}

// answer is what the user sees of reply: its text, or why fx failed.
func answer(reply runner.Reply, resumed bool) string {
	if text, ok := fxErrors[reply.Error]; ok {
		return text
	}
	switch {
	case reply.Error != "":
		text := "fx failed: " + reply.Error
		if line := lastLine(reply.Stderr); line != "" {
			text += "\n" + line
		}
		if resumed {
			text += "\nSend /new if this session cannot continue."
		}
		return text
	case reply.Output == "":
		return "(no reply)"
	}
	return reply.Output
}

// setMenu sends the command menu and reports whether Telegram took it. A
// failure is retried on the next poll.
func (c *Chat) setMenu(ctx context.Context, telegram *notify.Telegram) bool {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.SetCommands(call, commands); err != nil {
		log.Printf("chat: commands: %v", err)
		return false
	}
	return true
}

func (c *Chat) send(ctx context.Context, telegram *notify.Telegram, text string) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.Send(call, text); err != nil {
		log.Printf("chat: send: %v", err)
	}
}

func (c *Chat) editPlain(ctx context.Context, telegram *notify.Telegram, id int64, text string) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.Edit(call, id, text); err != nil {
		log.Printf("chat: edit: %v", err)
	}
}

func (c *Chat) sendHTML(ctx context.Context, telegram *notify.Telegram, text string, rows [][]notify.Button) int64 {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	id, err := telegram.SendHTML(call, text, rows)
	if err != nil {
		log.Printf("chat: send: %v", err)
	}
	return id
}

func (c *Chat) editHTML(ctx context.Context, telegram *notify.Telegram, id int64, text string, rows [][]notify.Button) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.EditHTML(call, id, text, rows); err != nil {
		log.Printf("chat: edit: %v", err)
	}
}

func (c *Chat) sendButtons(ctx context.Context, telegram *notify.Telegram, text string, rows [][]notify.Button) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := telegram.SendButtons(call, text, rows); err != nil {
		log.Printf("chat: send: %v", err)
	}
}

// command returns the bot command that text starts with, without a bot name
// suffix, and the text after it on the same line. name is "" when text is
// not a command.
func command(text string) (name, arg string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return "", ""
	}
	name, arg, _ = strings.Cut(text, " ")
	name, _, _ = strings.Cut(name, "@")
	return strings.ToLower(name), strings.TrimSpace(arg)
}

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
