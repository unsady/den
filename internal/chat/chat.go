// Package chat lets the user talk to the agent through the Telegram bot.
//
// Messages from the configured chat become turns of one fx session, run one
// at a time with fx ask --resume. Messages that arrive during a turn wait and
// go together as the next turn. Messages from any other chat are ignored.
//
// Photos, voice messages, and files are saved in the inbox as the agent, and
// the turn names their paths. A photo is also attached to the turn as an
// image, and a voice message comes with its transcript.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/sessions"
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
	{Name: "again", Description: "Ask the second model about your last message, or a new one"},
	{Name: "stop", Description: "Stop the current turn"},
	{Name: "compact", Description: "Shrink the session to a summary"},
	{Name: "jobs", Description: "List, run, or remove jobs"},
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
	StartJob(ref string) (jobs.Job, error)
	// Answer passes the user's answer to question id, one of approve's
	// outcomes, and returns the question, or false when it is no longer
	// waiting.
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
	// JobSessions are the sessions of the job runs den keeps.
	JobSessions() map[string]bool
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

type state struct {
	// Session is the fx session the chat continues, empty before the first turn.
	Session string `json:"session"`
	// Offset is the next Telegram update to read.
	Offset int64 `json:"offset"`
	// Provider and Model are what /model chose, empty for fx's own choice.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Opened are the sessions the chat used last, newest first, for the
	// order of /sessions.
	Opened []string `json:"opened,omitempty"`
	// Recent are the models /model chose last, as provider/model, newest
	// first.
	Recent []string `json:"recent,omitempty"`
	// Compacted is the session's size before den compacted it while the
	// user was away, until the next reply tells them.
	Compacted int `json:"compacted,omitempty"`
	// Effort is what /effort chose, empty for the model's own default.
	Effort string `json:"effort,omitempty"`
	// Context is the session's size after its last turn, in tokens.
	Context int `json:"context,omitempty"`
	// Second is the second model, which jobs can run on, empty until /model
	// chooses one.
	Second *runner.Choice `json:"second,omitempty"`
	// Replies tie the bot's latest messages to the fx session that wrote
	// them, oldest first, so a reply to one continues that session.
	Replies []sent `json:"replies,omitempty"`
}

// sent is one message of the bot's and the session that wrote it.
type sent struct {
	Message int64  `json:"message"`
	Session string `json:"session"`
	// Label names the session above its replies, such as a job's heading;
	// empty for the chat's own sessions.
	Label string `json:"label,omitempty"`
}

// keptReplies is how many of the bot's latest messages a reply can go back
// to.
const keptReplies = 500

// entry is one message waiting for the next turn.
type entry struct {
	text string
	// image is the saved path of a photo to attach.
	image string
	// second runs the turn on the second model, for /again.
	second bool
	// report marks den's report of what came of an action, and notice is
	// what den tells the user instead when the agent does not get it, after
	// /stop or a switch; nothing when it is empty.
	report bool
	notice string
	// session is where the message goes when it is not the chat's own
	// session, such as a reply to a job's message, and label names that
	// session above the answer.
	session string
	label   string
}

// againNote asks the second model to answer the last message again.
const againNote = "[The user asked you, a different model, to answer their last message again. " +
	"The answer above came from another model: check it, correct it, and give your own best answer.]"

type Chat struct {
	cfg   config.Config
	path  string
	den   Daemon
	usage *usage.Store

	mu    sync.Mutex
	state state
	// generation changes on /new, so a turn that was running then does not
	// put its session back.
	generation int
	inbox      []entry
	stop       context.CancelFunc
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
	// turns are the latest turns' IDs with their generation, so den can
	// tell the agent of the session a turn ran in what came of a command
	// it started.
	turns []turnRef
	// retry is the last turn whose model's provider failed, which a button
	// under the failure can ask the second model again.
	retry *entry
	// bg stops each turn that /bg runs, by its ID, while it runs, and
	// background waits for them to end.
	bg         map[string]context.CancelFunc
	background sync.WaitGroup
}

type turnRef struct {
	id         string
	generation int
	// session and label are set for a turn in a session other than the
	// chat's own.
	session string
	label   string
	// bg marks a /bg turn, whose session is known only once it ends;
	// reports for it wait in pending until then.
	bg      bool
	pending []entry
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

// Tell gives the agent text as a new turn, such as what came of a command
// that waited for the user after the agent's turn ended. It returns false
// when turn is not one of the chat's in its current session, such as a
// job's, and the caller tells the user instead.
func (c *Chat) Tell(turn, text, notice string) bool {
	c.mu.Lock()
	found, wake := false, false
	for i, t := range c.turns {
		if t.id != turn {
			continue
		}
		e := entry{text: text, report: true, notice: notice, session: t.session, label: t.label}
		switch {
		case t.bg && t.session == "":
			found = true
			c.turns[i].pending = append(t.pending, e)
		// A switch leaves another session's turns where they are.
		case t.session != "" || t.generation == c.generation:
			found, wake = true, true
			c.inbox = append(c.inbox, e)
		}
		break
	}
	c.mu.Unlock()
	if wake {
		c.poke()
	}
	return found
}

// poke wakes the worker to run what waits in the inbox.
func (c *Chat) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func New(cfg config.Config, den Daemon, usage *usage.Store) *Chat {
	c := &Chat{
		cfg:     cfg,
		path:    filepath.Join(cfg.State, "chat.json"),
		den:     den,
		usage:   usage,
		wake:    make(chan struct{}, 1),
		squeeze: make(chan struct{}, 1),
		notes:   make(chan string, 1),
		bg:      map[string]context.CancelFunc{},
	}

	data, err := os.ReadFile(c.path)
	if err == nil {
		err = json.Unmarshal(data, &c.state)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("chat: %v", err)
	}
	return c
}

// Run reads the chat and runs its turns until ctx ends and the current turn
// has stopped.
func (c *Chat) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { c.work(ctx) })
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
	if arg != "" && name != "/model" && name != "/effort" && name != "/again" && name != "/sessions" {
		name = ""
	}
	switch name {
	case "/start", "/help":
		c.send(ctx, telegram, help())

	case "/new":
		c.tell(ctx, telegram, c.switchTo(""))
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

	case "/again":
		c.again(ctx, telegram, m, arg)

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
		c.mu.Lock()
		stop := c.stop
		notices := c.dropReports(true)
		c.inbox = nil
		var stops []context.CancelFunc
		for _, s := range c.bg {
			stops = append(stops, s)
		}
		c.mu.Unlock()
		if stop != nil {
			stops = append(stops, stop)
		}
		if len(stops) == 0 {
			c.send(ctx, telegram, "Nothing is running.")
		} else {
			for _, s := range stops {
				s()
			}
			c.send(ctx, telegram, "Stopped.")
		}
		c.tell(ctx, telegram, notices)

	default:
		c.queue(ctx, telegram, m, entry{text: quoted(m, m.Text)})
	}
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

// switchTo makes the next turn continue session, or start a new one when it
// is empty. A turn that is running then does not put its own session back.
// It returns what den's waiting reports would have told the old session's
// agent, for the user.
func (c *Chat) switchTo(session string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	notices := c.dropReports(false)
	c.state.Session = session
	c.opened(session)
	c.state.Context = 0
	c.state.Compacted = 0
	c.generation++
	c.save()
	return notices
}

// dropReports takes den's reports for the chat's session, or all of them,
// out of the inbox and returns what to tell the user about them instead.
// The caller holds c.mu.
func (c *Chat) dropReports(all bool) []string {
	var notices []string
	kept := c.inbox[:0]
	for _, e := range c.inbox {
		if !e.report || (!all && e.session != "") {
			kept = append(kept, e)
		} else if e.notice != "" {
			notices = append(notices, e.notice)
		}
	}
	c.inbox = kept
	return notices
}

// tell sends the user den's reports their agent did not get.
func (c *Chat) tell(ctx context.Context, telegram *notify.Telegram, notices []string) {
	for _, n := range notices {
		c.send(ctx, telegram, n)
	}
}

// openedSessions is how many sessions the chat remembers using.
const openedSessions = 30

// opened puts session first among the ones the chat used. c.mu is held.
func (c *Chat) opened(session string) {
	if session == "" {
		return
	}
	list := []string{session}
	for _, id := range c.state.Opened {
		if id != session && len(list) < openedSessions {
			list = append(list, id)
		}
	}
	c.state.Opened = list
}

// queue keeps a message for the next turn. While a turn runs, it marks the
// message with 👀, so the user sees it waits.
func (c *Chat) queue(ctx context.Context, telegram *notify.Telegram, m notify.Message, e entry) {
	id := m.ID
	c.mu.Lock()
	if e.session == "" {
		e.session, e.label = c.other(m.ReplyTo)
	}
	c.inbox = append(c.inbox, e)
	busy := c.stop != nil
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}

	if busy && id != 0 {
		call, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := telegram.React(call, id, "👀"); err != nil {
			log.Printf("chat: react: %v", err)
		}
	}
}

// Sent ties the bot's messages ids to session, which wrote them, so a reply
// to one continues it. label names the session above its replies.
func (c *Chat) Sent(ids []int64, session, label string) {
	if session == "" || len(ids) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.state.Replies = append(c.state.Replies, sent{Message: id, Session: session, Label: label})
	}
	if len(c.state.Replies) > keptReplies {
		c.state.Replies = c.state.Replies[len(c.state.Replies)-keptReplies:]
	}
	c.save()
}

// other returns the session that wrote the bot's message id and its label,
// when that session is not the chat's own. The caller holds c.mu.
func (c *Chat) other(id int64) (session, label string) {
	if id == 0 {
		return "", ""
	}
	for i := len(c.state.Replies) - 1; i >= 0; i-- {
		r := c.state.Replies[i]
		if r.Message != id {
			continue
		}
		if r.Session == c.state.Session {
			return "", ""
		}
		return r.Session, r.Label
	}
	return "", ""
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
	c.mu.Lock()
	if len(c.inbox) == 0 {
		c.mu.Unlock()
		return false
	}
	// Messages for another session, such as replies to a job's message,
	// wait for a turn of their own.
	other, label := c.inbox[0].session, c.inbox[0].label
	var texts, images []string
	var rest []entry
	// A turn started by den's reports alone, such as that a den map run
	// ended, may end without a word to the user.
	second, reports := false, true
	for _, e := range c.inbox {
		if e.session != other {
			rest = append(rest, e)
			continue
		}
		reports = reports && e.report
		texts = append(texts, e.text)
		if e.image != "" {
			images = append(images, e.image)
		}
		second = second || e.second
	}
	prompt := strings.Join(texts, "\n\n")
	c.inbox = rest
	session := c.state.Session
	if other != "" {
		session = other
	}
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	// A reply from the second model says so, since the chat goes on with
	// the main one.
	header := ""
	fallback := c.state.Second
	if second && fallback != nil {
		provider, model, effort = fallback.Provider, fallback.Model, fallback.Effort
		header = "↻ " + model + "\n\n"
		fallback = nil
	}
	generation := c.generation
	turn, stop := context.WithCancel(ctx)
	c.stop = stop
	current := rand.Text()
	c.turnID = current
	c.turns = append(c.turns, turnRef{id: current, generation: generation, session: other, label: label})
	if len(c.turns) > keptTurns {
		c.turns = c.turns[len(c.turns)-keptTurns:]
	}
	c.mu.Unlock()
	defer stop()
	started := time.Now()

	done := make(chan struct{})
	shown := make(chan struct{})
	steps := make(chan string, 64)
	go c.showTyping(turn, done)
	go func() {
		c.showProgress(ctx, steps, done)
		close(shown)
	}()
	ask := func(provider, model, effort, resume string) runner.Reply {
		return runner.Ask(turn, c.cfg, runner.Request{
			Prompt:     prompt,
			Provider:   provider,
			Model:      model,
			Effort:     effort,
			Resume:     resume,
			Images:     images,
			Turn:       current,
			MaySilence: true,
			Progress: func(line string) {
				select {
				case steps <- line:
				default:
				}
			},
		})
	}
	reply := ask(provider, model, effort, session)
	// When the main model's provider refuses or fails, such as when its
	// quota runs out, a button offers the second model. It does not take
	// over by itself: the user may want this model's answer, not any.
	var offer [][]notify.Button
	if reply.Error != "" && turn.Err() == nil && fallback != nil && runner.ProviderFailed(reply.Stderr) {
		offer = [][]notify.Button{{{Text: "↻ Ask " + fallback.Model, Data: "rs:"}}}
		c.mu.Lock()
		c.retry = &entry{text: prompt, second: true, session: other, label: label}
		if len(images) > 0 {
			c.retry.image = images[0]
		}
		c.mu.Unlock()
	}
	close(done)
	<-shown

	c.mu.Lock()
	c.stop = nil
	c.turnID = ""
	compacted := ""
	switched := generation != c.generation
	if other == "" && !switched && reply.SessionID != "" {
		c.state.Session = reply.SessionID
		c.opened(reply.SessionID)
		if size := c.contextSize(started); size > 0 {
			c.state.Context = size
		}
		if c.state.Compacted > 0 && reply.Error == "" {
			compacted = "🗜 While you were away, den compacted this session"
			if c.state.Context > 0 {
				compacted += fmt.Sprintf(": %s → %s tokens", tokens(c.state.Compacted), tokens(c.state.Context))
			}
			compacted += ".\n\n"
			c.state.Compacted = 0
		}
		c.save()
	}
	c.mu.Unlock()

	log.Printf("chat: turn in %s", reply.Duration)
	if reply.InputTokens > 0 || reply.OutputTokens > 0 {
		c.usage.Add("chat", time.Now(), reply.InputTokens, reply.OutputTokens)
	}
	if turn.Err() == context.Canceled {
		// /stop already answered, or den is shutting down.
		return true
	}
	// A turn may end without a word when den's report started it, or when
	// it left work going on that den shows the user anyway. A user's
	// question gets an answer otherwise.
	if reply.Error == "" && strings.TrimSpace(reply.Output) == runner.Silent {
		if reports || (c.den != nil && c.den.Busy(current)) {
			log.Printf("chat: the turn had nothing to tell")
			return true
		}
		reply.Output = sessions.Answer(c.cfg.Sessions, reply.SessionID, started)
	}

	text := answer(reply, session != "")
	if offer != nil {
		text = fmt.Sprintf("%s failed: %s", model, clipRunes(lastLine(reply.Stderr), 200))
	}
	wrote := reply.SessionID
	if wrote == "" {
		wrote = session
	}
	// A reply from a session other than the chat's, such as a reply to a
	// job's message or a turn the chat left with /new, says which.
	if other != "" || switched {
		if label == "" {
			label = c.titled(wrote)
		}
		text = label + "\n\n" + text
	}
	text = compacted + header + text

	if telegram, err := notify.Load(c.cfg); err == nil {
		call, cancel := context.WithTimeout(ctx, time.Minute)
		var ids []int64
		if offer != nil {
			var id int64
			id, err = telegram.SendButtons(call, text, offer)
			ids = []int64{id}
		} else {
			ids, err = telegram.SendIDs(call, text)
		}
		cancel()
		if err != nil {
			log.Printf("chat: send: %v", err)
		}
		c.Sent(ids, wrote, label)
	}
	return true
}

// answer is what the user sees of reply: its text, or why fx failed.
func answer(reply runner.Reply, resumed bool) string {
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

// titled names session above its replies by its title, cut short.
func (c *Chat) titled(session string) string {
	title := "Earlier session"
	list, err := sessions.List(c.cfg.Sessions, 1<<20)
	if err != nil {
		return "↩ " + title
	}
	for _, s := range list {
		if s.ID == session && s.Title != "Untitled session" {
			title = clipRunes(s.Title, 40)
		}
	}
	return "↩ " + title
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

func (c *Chat) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Session
}

func (c *Chat) offset() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Offset
}

// setOffset records that the updates before next were read, before their
// turns run, so a turn that crashes den is not read again on restart.
func (c *Chat) setOffset(next int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if next == c.state.Offset {
		return
	}
	c.state.Offset = next
	c.save()
}

// save writes the state. The caller holds c.mu.
func (c *Chat) save() {
	data, _ := json.Marshal(c.state)
	tmp := c.path + ".tmp"
	err := os.WriteFile(tmp, data, 0o600)
	if err == nil {
		err = os.Rename(tmp, c.path)
	}
	if err != nil {
		log.Printf("chat: save: %v", err)
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
