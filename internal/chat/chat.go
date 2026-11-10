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

// footer ends a session's first turn. den's notes follow the user's words
// rather than precede them, because fx names a session after the start of
// its first message when the provider has no model for titles.
const footer = "\n\n[The user is writing to you from Telegram. " +
	"Your final reply is sent back to that chat as plain text, so keep it short and skip Markdown formatting.]"

// commands fill the bot's command menu and the help message.
var commands = []notify.Command{
	{Name: "new", Description: "Start a new session"},
	{Name: "sessions", Description: "Switch to a recent session"},
	{Name: "model", Description: "Choose the model and provider"},
	{Name: "effort", Description: "Choose how hard the model thinks"},
	{Name: "again", Description: "Ask the second model about your last message, or a new one"},
	{Name: "stop", Description: "Stop the current turn"},
	{Name: "jobs", Description: "List, run, or remove jobs"},
	{Name: "log", Description: "What the agent did outside today, and what you always allow"},
	{Name: "status", Description: "Show the model, context, memory, and jobs"},
	{Name: "usage", Description: "Show the tokens den used"},
	{Name: "help", Description: "Show what the bot does"},
}

// recentSessions is how many sessions /sessions offers.
const recentSessions = 8

// Daemon is what the chat needs from the rest of den: its jobs, and the
// questions den asked the user.
type Daemon interface {
	ListJobs() []jobs.Job
	StartJob(ref string) (jobs.Job, error)
	RemoveJob(ref string) (jobs.Job, error)
	SetJobSlot(ref, slot string) (jobs.Job, error)
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
}

// entry is one message waiting for the next turn.
type entry struct {
	text string
	// image is the saved path of a photo to attach.
	image string
	// second runs the turn on the second model, for /again.
	second bool
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
}

func New(cfg config.Config, den Daemon, usage *usage.Store) *Chat {
	c := &Chat{
		cfg:   cfg,
		path:  filepath.Join(cfg.State, "chat.json"),
		den:   den,
		usage: usage,
		wake:  make(chan struct{}, 1),
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

	name, arg := command(m.Text)
	if arg != "" && name != "/model" && name != "/effort" && name != "/again" {
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

	case "/again":
		c.again(ctx, telegram, m, arg)

	case "/sessions":
		c.listSessions(ctx, telegram)

	case "/jobs":
		c.listJobs(ctx, telegram, 0)

	case "/log":
		c.showLog(ctx, telegram, 0)

	case "/status":
		c.sendHTML(ctx, telegram, c.status(ctx), nil)

	case "/usage":
		c.send(ctx, telegram, c.usage.Report(time.Now()))

	case "/stop":
		c.mu.Lock()
		stop := c.stop
		c.inbox = nil
		c.mu.Unlock()
		if stop == nil {
			c.send(ctx, telegram, "Nothing is running.")
			return
		}
		stop()
		c.send(ctx, telegram, "Stopped.")

	default:
		c.queue(ctx, telegram, m.ID, entry{text: quoted(m, m.Text)})
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
func (c *Chat) switchTo(session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Session = session
	c.state.Context = 0
	c.state.Compacted = 0
	c.generation++
	c.save()
}

// queue keeps a message for the next turn. While a turn runs, it marks the
// message with 👀, so the user sees it waits.
func (c *Chat) queue(ctx context.Context, telegram *notify.Telegram, id int64, e entry) {
	c.mu.Lock()
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

// turn runs the waiting messages as one turn. It returns false when there
// were none.
func (c *Chat) turn(ctx context.Context) bool {
	c.mu.Lock()
	if len(c.inbox) == 0 {
		c.mu.Unlock()
		return false
	}
	var texts, images []string
	second := false
	for _, e := range c.inbox {
		texts = append(texts, e.text)
		if e.image != "" {
			images = append(images, e.image)
		}
		second = second || e.second
	}
	prompt := strings.Join(texts, "\n\n")
	c.inbox = nil
	session := c.state.Session
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
	c.mu.Unlock()
	defer stop()
	started := time.Now()

	if session == "" {
		prompt += footer
	}

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
			Prompt:   prompt,
			Provider: provider,
			Model:    model,
			Effort:   effort,
			Resume:   resume,
			Images:   images,
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
	// quota runs out, the second model takes the turn once.
	if reply.Error != "" && turn.Err() == nil && fallback != nil && runner.ProviderFailed(reply.Stderr) {
		log.Printf("chat: %s failed, asking %s", model, fallback.Model)
		header = fmt.Sprintf("↻ %s · %s failed: %s\n\n", fallback.Model, model, clipRunes(lastLine(reply.Stderr), 120))
		resume := session
		if reply.SessionID != "" {
			resume = reply.SessionID
		}
		reply = ask(fallback.Provider, fallback.Model, fallback.Effort, resume)
	}
	close(done)
	<-shown

	c.mu.Lock()
	c.stop = nil
	compacted := ""
	if generation == c.generation && reply.SessionID != "" {
		c.state.Session = reply.SessionID
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

	text := reply.Output
	switch {
	case reply.Error != "":
		text = "fx failed: " + reply.Error
		if line := lastLine(reply.Stderr); line != "" {
			text += "\n" + line
		}
		if session != "" {
			text += "\nSend /new if this session cannot continue."
		}
	case text == "":
		text = "(no reply)"
	}
	text = compacted + header + text

	if telegram, err := notify.Load(c.cfg); err == nil {
		c.send(ctx, telegram, text)
	}
	return true
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
